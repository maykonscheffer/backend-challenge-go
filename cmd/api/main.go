package main

import (
	"context"
	"example.com/wallet/internal/bootstrap"
	"example.com/wallet/migrations"
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) != 3 || os.Args[1] != "migrate" {
			fmt.Fprintln(os.Stderr, "usage: api [migrate up|down]")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := migrations.Run(ctx, os.Getenv("DATABASE_URL"), os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	bootstrap.New().Run()
}
