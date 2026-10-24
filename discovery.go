package discovery

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

const DiscoveryPort = 49152
const SyncPort = 49153
const BroadcastInterval = 2 * time.Second

type Peer struct {
	IP         string
	DeviceName string
	LastSeen   time.Time
}

type AnnounceMsg struct {
	DeviceName string `json:"device_name"`
	SyncPort   int    `json:"sync_port"`
}

type DiscoveryService struct {
	deviceName string
	peers      map[string]*Peer
	mu         sync.Mutex
	stopChan   chan struct{}
}

func NewDiscoveryService(deviceName string) *DiscoveryService {
	return &DiscoveryService{
		deviceName: deviceName,
		peers:      make(map[string]*Peer),
		stopChan:   make(chan struct{}),
	}
}

func (ds *DiscoveryService) Start() error {
	go ds.listenBroadcasts()
	go ds.sendBroadcasts()
	return nil
}

func (ds *DiscoveryService) Stop() {
	close(ds.stopChan)
}

func (ds *DiscoveryService) GetPeers() []Peer {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	peers := make([]Peer, 0, len(ds.peers))
	now := time.Now()
	for _, p := range ds.peers {
		if now.Sub(p.LastSeen) < 10*time.Second {
			peers = append(peers, *p)
		}
	}
	return peers
}

func (ds *DiscoveryService) sendBroadcasts() {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("255.255.255.255:%d", DiscoveryPort))
	if err != nil {
		fmt.Printf("Error resolving UDP address: %v\n", err)
		return
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("Error dialing UDP: %v\n", err)
		return
	}
	defer conn.Close()

	msg := AnnounceMsg{
		DeviceName: ds.deviceName,
		SyncPort:   SyncPort,
	}
	data, _ := json.Marshal(msg)

	ticker := time.NewTicker(BroadcastInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ds.stopChan:
			return
		case <-ticker.C:
			_, err := conn.Write(data)
			if err != nil {
				fmt.Printf("Error sending broadcast: %v\n", err)
			}
		}
	}
}

func (ds *DiscoveryService) listenBroadcasts() {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", DiscoveryPort))
	if err != nil {
		fmt.Printf("Error resolving UDP listen address: %v\n", err)
		return
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Printf("Error listening UDP: %v\n", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)

	// Since ListenUDP doesn't easily stop via channel without closing, we use a goroutine trick or set deadlines
	for {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, remoteAddr, err := conn.ReadFromUDP(buf)

		select {
		case <-ds.stopChan:
			return
		default:
		}

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			fmt.Printf("Error reading broadcast: %v\n", err)
			continue
		}

		var msg AnnounceMsg
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}

		// Don't add ourselves
		if msg.DeviceName == ds.deviceName {
			continue
		}

		ds.mu.Lock()
		ip := remoteAddr.IP.String()
		if peer, exists := ds.peers[ip]; exists {
			peer.LastSeen = time.Now()
			peer.DeviceName = msg.DeviceName
		} else {
			ds.peers[ip] = &Peer{
				IP:         ip,
				DeviceName: msg.DeviceName,
				LastSeen:   time.Now(),
			}
			fmt.Printf("Discovered peer: %s at %s\n", msg.DeviceName, ip)
		}
		ds.mu.Unlock()
	}
}
