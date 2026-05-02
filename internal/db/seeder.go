package db

import (
	"strings"

	"bebii-seo-dashboard/internal/auth"
	"gorm.io/gorm"
)

const (
	defaultSuperAdminEmail    = "admin@bebii.com"
	defaultSuperAdminPassword = "admin123"
	defaultUserEmail          = "user@bebii.com"
	defaultUserPassword       = "user123"
)

func SeedDefaultData(conn *gorm.DB) error {
	if err := SeedDefaultAdmin(conn); err != nil {
		return err
	}
	if err := SeedDefaultUser(conn); err != nil {
		return err
	}
	return nil
}

func SeedDefaultAdmin(conn *gorm.DB) error {
	return seedUserIfMissing(conn, defaultSuperAdminEmail, defaultSuperAdminPassword, "admin")
}

func SeedDefaultUser(conn *gorm.DB) error {
	return seedUserIfMissing(conn, defaultUserEmail, defaultUserPassword, "user")
}

func seedUserIfMissing(conn *gorm.DB, email, password, role string) error {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))

	var count int64
	if err := conn.Model(&User{}).Where("email = ?", normalizedEmail).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	licenseKey, err := NewLicenseKey()
	if err != nil {
		return err
	}

	return conn.Create(&User{
		Email:        normalizedEmail,
		PasswordHash: hash,
		LicenseKey:   licenseKey,
		Role:         role,
		IsActive:     true,
	}).Error
}
