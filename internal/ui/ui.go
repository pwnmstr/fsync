package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"filesync/internal/config"
	"filesync/internal/discovery"
	"filesync/internal/sync"
)

type AppUI struct {
	App         fyne.App
	Window      fyne.Window
	Cfg         *config.AppConfig
	DiscoverySvc *discovery.DiscoveryService
	SyncSvc     *sync.SyncService
}

func NewAppUI(a fyne.App, cfg *config.AppConfig, discSvc *discovery.DiscoveryService, syncSvc *sync.SyncService) *AppUI {
	w := a.NewWindow("File Sync")

	return &AppUI{
		App:          a,
		Window:       w,
		Cfg:          cfg,
		DiscoverySvc: discSvc,
		SyncSvc:      syncSvc,
	}
}

func (ui *AppUI) Run() {
	statusLabel := widget.NewLabel("Starting services...")
	peersList := widget.NewList(
		func() int { return 0 },
		func() fyne.CanvasObject { return widget.NewLabel("Template") },
		func(i widget.ListItemID, o fyne.CanvasObject) {},
	)

	mappingsStr := ""
	for _, m := range ui.Cfg.Mappings {
		mappingsStr += fmt.Sprintf("%s -> %s\n", m.ID, m.LocalPath)
	}
	if mappingsStr == "" {
		mappingsStr = "No directories configured to sync. Edit config.json."
	}
	mappingsLabel := widget.NewLabel("Configured Mappings:\n" + mappingsStr)

	syncButton := widget.NewButton("Force Sync All", func() {
		peers := ui.DiscoverySvc.GetPeers()
		if len(peers) == 0 {
			statusLabel.SetText("No peers found to sync with.")
			return
		}
		statusLabel.SetText("Syncing with peers...")
		go func() {
			for _, p := range peers {
				err := ui.SyncSvc.PerformSync(p.IP, discovery.SyncPort)
				if err != nil {
					fmt.Printf("Sync error with %s: %v\n", p.IP, err)
				}
			}
			statusLabel.SetText("Sync complete.")
		}()
	})

	content := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Device: %s", ui.Cfg.DeviceName)),
		mappingsLabel,
		widget.NewLabel("Discovered Peers:"),
		peersList,
		statusLabel,
		syncButton,
	)

	ui.Window.SetContent(content)
	ui.Window.Resize(fyne.NewSize(400, 300))

	// Update peers list periodically
	go func() {
		for {
			time.Sleep(3 * time.Second)
			peers := ui.DiscoverySvc.GetPeers()
			peersList.Length = func() int { return len(peers) }
			peersList.UpdateItem = func(i widget.ListItemID, o fyne.CanvasObject) {
				o.(*widget.Label).SetText(fmt.Sprintf("%s (%s)", peers[i].DeviceName, peers[i].IP))
			}
			peersList.Refresh()
		}
	}()

	statusLabel.SetText("Services running. Listening for peers...")
	ui.Window.ShowAndRun()
}
