package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"distsrv/internal/auth"
	"distsrv/internal/config"
	"distsrv/internal/db"
	"distsrv/internal/server"
	"distsrv/internal/storage"
)

func main() {
	configPath := flag.String("config", "/etc/distsrv/config.toml", "path to config file")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[distsrv] ")

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("ensure dirs: %v", err)
	}

	database, err := db.Open(cfg.DB.Path, cfg.DB.BusyTimeoutMS)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if err := bootstrapAdmin(database, cfg); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	st := storage.New(cfg, database)

	if err := st.OrphanScan(); err != nil {
		log.Printf("warning: orphan scan: %v", err)
	}

	srv, err := server.New(cfg, database, st)
	if err != nil {
		log.Fatalf("server init: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		s := <-sig
		log.Printf("received signal %s, shutting down", s)
		cancel()
	}()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Printf("shutdown complete")
}

func bootstrapAdmin(d *db.DB, cfg *config.Config) error {
	n, err := d.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := auth.HashPassword(cfg.Admin.Password, cfg.Security.BcryptCost)
	if err != nil {
		return err
	}
	id, err := d.CreateUser(cfg.Admin.Username, hash, true)
	if err != nil {
		return err
	}
	log.Printf("=========================================================")
	log.Printf(" BOOTSTRAP: created initial admin user '%s' (id=%d)", cfg.Admin.Username, id)
	log.Printf(" Login at /admin/login and CHANGE THE PASSWORD immediately.")
	log.Printf(" Then remove [admin].password from config.toml for safety.")
	log.Printf("=========================================================")
	return nil
}

