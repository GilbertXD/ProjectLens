package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"projectlens/migrations"
)

func migrationProvider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	closeDB := func() { _ = sqlDB.Close() }
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	return provider, closeDB, nil
}

func MigrateUp(ctx context.Context, pool *pgxpool.Pool) error {
	provider, closeDB, err := migrationProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	_, err = provider.Up(ctx)
	return err
}

func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	provider, closeDB, err := migrationProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	_, err = provider.Down(ctx)
	return err
}

func MigrateStatus(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationStatus, error) {
	provider, closeDB, err := migrationProvider(pool)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return provider.Status(ctx)
}
