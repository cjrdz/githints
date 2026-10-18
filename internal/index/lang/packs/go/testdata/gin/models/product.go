package models

import "gorm.io/gorm"

type Product struct {
	gorm.Model
	Name string
}

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&Product{})
}
