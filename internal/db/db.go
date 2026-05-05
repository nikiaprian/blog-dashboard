package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"bebii-seo-dashboard/internal/auth"
	_ "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

type User struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	Email        string    `gorm:"size:255;uniqueIndex;not null"`
	PasswordHash string    `gorm:"column:password_hash;size:255;not null"`
	LicenseKey   string    `gorm:"column:license_key;size:128;not null"`
	Role         string    `gorm:"type:enum('admin','user');not null"`
	IsActive     bool      `gorm:"column:is_active;not null;default:true"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
}

type Domain struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	UserID    int64     `gorm:"column:user_id;not null;uniqueIndex:idx_user_domain"`
	Domain    string    `gorm:"size:255;not null;uniqueIndex:idx_user_domain"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

type Session struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	UserID    int64     `gorm:"column:user_id;not null;index"`
	Token     string    `gorm:"size:255;uniqueIndex;not null"`
	ExpiresAt time.Time `gorm:"column:expires_at;not null"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

type VerifyLog struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	Domain     string    `gorm:"size:255;index;not null"`
	LicenseKey string    `gorm:"column:license_key;size:128;index"`
	Success    bool      `gorm:"not null"`
	Message    string    `gorm:"size:255;not null"`
	StatusCode int       `gorm:"column:status_code;not null"`
	IPAddress  string    `gorm:"column:ip_address;size:64"`
	UserAgent  string    `gorm:"column:user_agent;size:255"`
	Source     string    `gorm:"size:50"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime;index"`
}

type RemoteServer struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	Name       string    `gorm:"size:100;not null"`
	UserID     int64     `gorm:"column:user_id;index;not null"`
	User       *User     `gorm:"foreignKey:UserID"`
	Host       string    `gorm:"size:255;not null"`
	Port       int       `gorm:"not null;default:22"`
	SSHUser    string    `gorm:"column:ssh_user;size:100;not null"`
	SSHKeyID   int64     `gorm:"column:ssh_key_id;index"`
	SSHKey     *SSHKey   `gorm:"foreignKey:SSHKeyID"`
	SSHKeyPath string    `gorm:"column:ssh_key_path;size:255;not null"`
	IsEnabled  bool      `gorm:"column:is_enabled;not null;default:true"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
}

type SSHKey struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	Name      string    `gorm:"size:100;uniqueIndex;not null"`
	KeyPath   string    `gorm:"column:key_path;size:255"`
	KeyBody   string    `gorm:"column:key_body;type:longtext"`
	IsEnabled bool      `gorm:"column:is_enabled;not null;default:true"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

type ServerCommandLog struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	ServerID     int64     `gorm:"column:server_id;index;not null"`
	ServerName   string    `gorm:"column:server_name;size:100;not null"`
	CommandInput string    `gorm:"column:command_input;size:50;not null"`
	Success      bool      `gorm:"not null"`
	Output       string    `gorm:"type:longtext"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime;index"`
}

// UserBlogRepoAck stores the last GitHub main-branch commit SHA the user acknowledged (dismissed banner).
type UserBlogRepoAck struct {
	UserID          int64     `gorm:"column:user_id;primaryKey"`
	AcknowledgedSHA string    `gorm:"column:acknowledged_sha;size:64;not null;default:''"`
	UpdatedAt       time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func OpenMySQL(cfg DBConfig) (*gorm.DB, error) {
	conn, err := gorm.Open(mysql.Open(buildDSN(cfg, true)), &gorm.Config{
		// Existing rows may contain legacy values that violate new FKs.
		// Keep application-level relations without forcing DB-level FK creation.
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, err
	}
	return conn, nil
}

func EnsureDatabaseExists(cfg DBConfig) error {
	sqlDB, err := sql.Open("mysql", buildDSN(cfg, false))
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if err := sqlDB.Ping(); err != nil {
		return err
	}
	_, err = sqlDB.Exec("CREATE DATABASE IF NOT EXISTS `" + cfg.Name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	return err
}

func Migrate(conn *gorm.DB) error {
	if err := conn.AutoMigrate(&User{}, &Domain{}, &Session{}, &VerifyLog{}, &SSHKey{}, &RemoteServer{}, &ServerCommandLog{}, &UserBlogRepoAck{}); err != nil {
		return err
	}
	return nil
}

func EnsureUserLicenseKeys(conn *gorm.DB) error {
	var users []User
	if err := conn.Where("license_key = '' OR license_key IS NULL").Find(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		key, err := NewLicenseKey()
		if err != nil {
			return err
		}
		if err := conn.Model(&User{}).Where("id = ?", u.ID).Update("license_key", key).Error; err != nil {
			return err
		}
	}

	// Ensure no duplicate non-empty license keys remain before unique index creation.
	type dupRow struct {
		LicenseKey string
		Count      int64
	}
	var duplicates []dupRow
	if err := conn.
		Model(&User{}).
		Select("license_key, COUNT(*) as count").
		Where("license_key <> ''").
		Group("license_key").
		Having("COUNT(*) > 1").
		Scan(&duplicates).Error; err != nil {
		return err
	}
	for _, d := range duplicates {
		var dupUsers []User
		if err := conn.Where("license_key = ?", d.LicenseKey).Order("id ASC").Find(&dupUsers).Error; err != nil {
			return err
		}
		// Keep first user key, regenerate the rest.
		for i := 1; i < len(dupUsers); i++ {
			key, err := NewLicenseKey()
			if err != nil {
				return err
			}
			if err := conn.Model(&User{}).Where("id = ?", dupUsers[i].ID).Update("license_key", key).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

func EnsureLicenseKeyUniqueIndex(conn *gorm.DB) error {
	const indexName = "idx_users_license_key"
	if conn.Migrator().HasIndex(&User{}, indexName) {
		return nil
	}
	return conn.Exec("CREATE UNIQUE INDEX " + indexName + " ON users(license_key)").Error
}

func Authenticate(conn *gorm.DB, email, plain string) (*User, error) {
	var u User
	err := conn.Where("email = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, errors.New("inactive user")
	}
	if err := auth.CheckPassword(u.PasswordHash, plain); err != nil {
		return nil, errors.New("invalid credentials")
	}
	return &u, nil
}

func CreateSession(conn *gorm.DB, userID int64, ttl time.Duration) (string, error) {
	token, err := auth.NewToken(32)
	if err != nil {
		return "", err
	}
	session := Session{
		UserID:    userID,
		Token:     token,
		ExpiresAt: time.Now().Add(ttl).UTC(),
	}
	return token, conn.Create(&session).Error
}

func DeleteSession(conn *gorm.DB, token string) error {
	return conn.Where("token = ?", token).Delete(&Session{}).Error
}

func GetUserBySession(conn *gorm.DB, token string) (*User, error) {
	var s Session
	err := conn.Where("token = ?", token).First(&s).Error
	if err != nil {
		return nil, err
	}
	if time.Now().After(s.ExpiresAt) {
		_ = DeleteSession(conn, token)
		return nil, gorm.ErrRecordNotFound
	}
	var u User
	if err := conn.First(&u, "id = ?", s.UserID).Error; err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, gorm.ErrRecordNotFound
	}
	return &u, nil
}

func ListUsers(conn *gorm.DB) ([]User, error) {
	var users []User
	err := conn.Order("created_at DESC").Find(&users).Error
	return users, err
}

func CreateUser(conn *gorm.DB, email, password, role string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	role = strings.ToLower(strings.TrimSpace(role))
	if role != "admin" && role != "user" {
		role = "user"
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	licenseKey, err := NewLicenseKey()
	if err != nil {
		return err
	}
	user := User{
		Email:        email,
		PasswordHash: hash,
		LicenseKey:   licenseKey,
		Role:         role,
		IsActive:     true,
	}
	return conn.Create(&user).Error
}

func ToggleUserActive(conn *gorm.DB, userID int64) error {
	var user User
	if err := conn.First(&user, "id = ?", userID).Error; err != nil {
		return err
	}
	if user.Role == "admin" {
		return errors.New("admin cannot be deactivated")
	}
	user.IsActive = !user.IsActive
	err := conn.Model(&user).Update("is_active", user.IsActive).Error
	return err
}

func ResetUserPassword(conn *gorm.DB, userID int64, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	err = conn.Model(&User{}).Where("id = ?", userID).Update("password_hash", hash).Error
	return err
}

func NormalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	if i := strings.Index(d, "/"); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimPrefix(d, "www.")
	d = strings.TrimSuffix(d, ".")
	return d
}

func IsValidDomain(raw string) bool {
	d := NormalizeDomain(raw)
	if d == "" || strings.Contains(d, " ") {
		return false
	}
	if net.ParseIP(d) != nil {
		return true
	}
	return strings.Contains(d, ".")
}

func AddDomain(conn *gorm.DB, userID int64, raw string) error {
	d := NormalizeDomain(raw)
	if !IsValidDomain(d) {
		return fmt.Errorf("invalid domain")
	}
	return conn.Create(&Domain{
		UserID: userID,
		Domain: d,
	}).Error
}

func DeleteDomain(conn *gorm.DB, domainID int64, ownerID int64, admin bool) error {
	if admin {
		return conn.Where("id = ?", domainID).Delete(&Domain{}).Error
	}
	return conn.Where("id = ? AND user_id = ?", domainID, ownerID).Delete(&Domain{}).Error
}

func ListDomainsByUser(conn *gorm.DB, userID int64) ([]Domain, error) {
	var out []Domain
	err := conn.Where("user_id = ?", userID).Order("created_at DESC").Find(&out).Error
	return out, err
}

func DomainAllowed(conn *gorm.DB, rawDomain string) (bool, error) {
	d := NormalizeDomain(rawDomain)
	var count int64
	err := conn.
		Table("domains d").
		Joins("JOIN users u ON u.id = d.user_id").
		Where("d.domain = ? AND u.is_active = ?", d, true).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func VerifyLicense(conn *gorm.DB, rawDomain, licenseKey string) (bool, error) {
	d := NormalizeDomain(rawDomain)
	licenseKey = strings.TrimSpace(licenseKey)
	if d == "" || licenseKey == "" {
		return false, nil
	}
	var count int64
	err := conn.
		Table("domains d").
		Joins("JOIN users u ON u.id = d.user_id").
		Where("d.domain = ? AND u.is_active = ? AND u.license_key = ?", d, true, licenseKey).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func ResetUserLicenseKey(conn *gorm.DB, userID int64) (string, error) {
	var user User
	if err := conn.First(&user, "id = ?", userID).Error; err != nil {
		return "", err
	}
	key, err := NewLicenseKey()
	if err != nil {
		return "", err
	}
	if err := conn.Model(&user).Update("license_key", key).Error; err != nil {
		return "", err
	}
	return key, nil
}

func GetUserByID(conn *gorm.DB, userID int64) (*User, error) {
	var out User
	if err := conn.First(&out, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func buildDSN(cfg DBConfig, withDatabase bool) string {
	base := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
	)
	if withDatabase {
		base += cfg.Name
	}
	return base + "?parseTime=true&charset=utf8mb4&loc=Local"
}

func NewLicenseKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "bebii-" + hex.EncodeToString(b), nil
}

func CreateVerifyLog(conn *gorm.DB, log VerifyLog) error {
	return conn.Create(&log).Error
}

func ListVerifyLogs(conn *gorm.DB, limit int) ([]VerifyLog, error) {
	if limit <= 0 {
		limit = 100
	}
	var logs []VerifyLog
	err := conn.Order("created_at DESC").Limit(limit).Find(&logs).Error
	return logs, err
}

func ClearVerifyLogs(conn *gorm.DB) error {
	return conn.Exec("DELETE FROM verify_logs").Error
}

func ListRemoteServers(conn *gorm.DB) ([]RemoteServer, error) {
	var out []RemoteServer
	err := conn.Preload("SSHKey").Preload("User").Order("created_at DESC").Find(&out).Error
	return out, err
}

func ListEnabledRemoteServers(conn *gorm.DB) ([]RemoteServer, error) {
	var out []RemoteServer
	err := conn.Preload("SSHKey").Preload("User").Where("is_enabled = ?", true).Order("created_at DESC").Find(&out).Error
	return out, err
}

func ListRemoteServersByUser(conn *gorm.DB, userID int64) ([]RemoteServer, error) {
	var out []RemoteServer
	err := conn.Preload("SSHKey").Preload("User").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&out).Error
	return out, err
}

func CreateRemoteServer(conn *gorm.DB, server RemoteServer) error {
	if server.Port <= 0 {
		server.Port = 22
	}
	return conn.Create(&server).Error
}

func UpdateRemoteServer(conn *gorm.DB, server RemoteServer) error {
	if server.ID <= 0 {
		return errors.New("invalid server id")
	}
	if server.Port <= 0 {
		server.Port = 22
	}
	updates := map[string]any{
		"name":       strings.TrimSpace(server.Name),
		"user_id":    server.UserID,
		"host":       strings.TrimSpace(server.Host),
		"port":       server.Port,
		"ssh_user":   strings.TrimSpace(server.SSHUser),
		"ssh_key_id": server.SSHKeyID,
	}
	return conn.Model(&RemoteServer{}).Where("id = ?", server.ID).Updates(updates).Error
}

func ListSSHKeys(conn *gorm.DB) ([]SSHKey, error) {
	var out []SSHKey
	err := conn.Order("created_at DESC").Find(&out).Error
	return out, err
}

func CreateSSHKey(conn *gorm.DB, key SSHKey) error {
	return conn.Create(&key).Error
}

// ErrSSHKeyInUse is returned when deleting an SSH key still referenced by remote_servers.
var ErrSSHKeyInUse = errors.New("ssh key is still assigned to one or more servers")

func DeleteSSHKey(conn *gorm.DB, id int64) error {
	var n int64
	if err := conn.Model(&RemoteServer{}).Where("ssh_key_id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrSSHKeyInUse
	}
	return conn.Delete(&SSHKey{}, id).Error
}

func ToggleRemoteServerEnabled(conn *gorm.DB, id int64) error {
	var s RemoteServer
	if err := conn.First(&s, "id = ?", id).Error; err != nil {
		return err
	}
	return conn.Model(&s).Update("is_enabled", !s.IsEnabled).Error
}

func DeleteRemoteServer(conn *gorm.DB, id int64) error {
	return conn.Where("id = ?", id).Delete(&RemoteServer{}).Error
}

func GetRemoteServerByIDForUser(conn *gorm.DB, id, userID int64) (*RemoteServer, error) {
	var out RemoteServer
	if err := conn.Preload("SSHKey").Preload("User").Where("id = ? AND user_id = ?", id, userID).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func CreateServerCommandLog(conn *gorm.DB, log ServerCommandLog) error {
	return conn.Create(&log).Error
}

func ListServerCommandLogs(conn *gorm.DB, limit int) ([]ServerCommandLog, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []ServerCommandLog
	err := conn.Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

// ListServerCommandLogsForUser returns command logs only for servers linked to the given user.
func ListServerCommandLogsForUser(conn *gorm.DB, userID int64, limit int) ([]ServerCommandLog, error) {
	if limit <= 0 {
		limit = 100
	}
	var ids []int64
	if err := conn.Model(&RemoteServer{}).Where("user_id = ?", userID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []ServerCommandLog{}, nil
	}
	var out []ServerCommandLog
	err := conn.Where("server_id IN ?", ids).Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

// ClearServerCommandLogsForUser deletes command log rows whose server belongs to the user (does not touch other users' logs).
func ClearServerCommandLogsForUser(conn *gorm.DB, userID int64) error {
	var ids []int64
	if err := conn.Model(&RemoteServer{}).Where("user_id = ?", userID).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return conn.Where("server_id IN ?", ids).Delete(&ServerCommandLog{}).Error
}

func ClearServerCommandLogs(conn *gorm.DB) error {
	return conn.Exec("DELETE FROM server_command_logs").Error
}

func GetUserBlogRepoAck(conn *gorm.DB, userID int64) (string, error) {
	var row UserBlogRepoAck
	err := conn.Where("user_id = ?", userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(row.AcknowledgedSHA), nil
}

func SetUserBlogRepoAck(conn *gorm.DB, userID int64, fullSHA string) error {
	fullSHA = strings.TrimSpace(fullSHA)
	row := UserBlogRepoAck{UserID: userID, AcknowledgedSHA: fullSHA}
	return conn.Save(&row).Error
}
