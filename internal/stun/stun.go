package stun

import (
	"log"
	"net"

	"github.com/pion/turn/v4"
)

// StartSTUNServer starts an embedded STUN server on the given address (e.g., "0.0.0.0:3478").
// Returns a cleanup function to close the server.
func StartSTUNServer(addr string) (func(), error) {
	udpListener, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}

	server, err := turn.NewServer(turn.ServerConfig{
		Realm: "media-platform",
		AuthHandler: func(username string, realm string, srcAddr net.Addr) ([]byte, bool) {
			return nil, false // STUN only — no TURN authentication needed
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{PacketConn: udpListener},
		},
	})
	if err != nil {
		udpListener.Close()
		return nil, err
	}

	log.Printf("[stun] server listening on %s", addr)
	return func() {
		server.Close()
		log.Println("[stun] server stopped")
	}, nil
}
