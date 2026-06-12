package database

import (
	"context"
	"testing"

	"github.com/star-inc/armi/pkgs/file"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestListAccessibleAppliesPermissionBeforePagination(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&gormFileRecord{},
		&gormFileGroup{},
		&gormFileGroupMember{},
		&gormFileGroupFile{},
		&gormTag{},
	); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repo := &GormFileRepository{db: db}
	records := []*file.FileRecord{
		{ID: "public", Filename: "public.txt", Hash: "hash-public", AuthorID: "author"},
		{ID: "hidden", Filename: "hidden.txt", Hash: "hash-hidden", AuthorID: "author", GroupIDs: []string{"hidden-group"}},
		{ID: "visible", Filename: "visible.txt", Hash: "hash-visible", AuthorID: "author", GroupIDs: []string{"visible-group"}},
	}
	for _, record := range records {
		if err := repo.Create(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&gormFileGroupMember{
		UserID:      "reader",
		FileGroupID: "visible-group",
		Permission:  int(file.GroupPermissionRead),
	}).Error; err != nil {
		t.Fatal(err)
	}

	firstPage, total, err := repo.ListAccessible(ctx, "reader", "", file.GroupPermissionRead, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondPage, secondTotal, err := repo.ListAccessible(ctx, "reader", "", file.GroupPermissionRead, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	if total != 2 || secondTotal != 2 {
		t.Fatalf("expected visible total 2, got %d and %d", total, secondTotal)
	}
	if len(firstPage) != 1 || len(secondPage) != 1 {
		t.Fatalf("expected full visible pages, got %d and %d items", len(firstPage), len(secondPage))
	}
	if firstPage[0].ID == "hidden" || secondPage[0].ID == "hidden" {
		t.Fatal("hidden file was returned")
	}
}
