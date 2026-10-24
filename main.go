package main

import (
	"fmt"
	"os"

	"filesync/internal/config"
	"filesync/internal/discovery"
	"filesync/internal/sync"
	"filesync/internal/ui"
)

func main() {
	cfgPath := "config.json"
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	discSvc := discovery.NewDiscoveryService(cfg.DeviceName)
	if err := discSvc.Start(); err != nil {
		fmt.Printf("Failed to start discovery: %v\n", err)
		os.Exit(1)
	}
	defer discSvc.Stop()

	syncSvc := sync.NewSyncService(cfg)
	syncSvc.StartServer(discovery.SyncPort)
	defer syncSvc.StopServer()

	appUI := ui.NewAppUI(cfg, discSvc, syncSvc)
	appUI.Run()
}
