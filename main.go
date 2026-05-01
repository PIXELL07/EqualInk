package main

import (
	"log"

	"github.com/pixell07/equalink/api"
	"github.com/pixell07/equalink/config"
)

func main() {
	cfg := config.Load()
	srv := api.NewServer(cfg)
	log.Printf("EqualInk running on %s", cfg.Port)
	log.Fatal(srv.Start())
}
