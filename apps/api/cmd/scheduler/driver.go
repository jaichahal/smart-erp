package main

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func outboxDriver(pool *pgxpool.Pool) riverdriver.Driver[pgx.Tx] { return riverpgxv5.New(pool) }
