package listener

import (
	"log"
	pcap "github.com/akrennmair/gopcap"
)

// Capture traffic from socket using RAW_SOCKET's
// http://en.wikipedia.org/wiki/Raw_socket
//
// RAW_SOCKET allow you listen for traffic on any port (e.g. sniffing) because they operate on IP level.
// Ports is TCP feature, same as flow control, reliable transmission and etc.
// Since we can't use default TCP libraries RAWTCPLitener implements own TCP layer
// TCP packets is parsed using tcp_packet.go, and flow control is managed by tcp_message.go
type RAWTCPListener struct {
	messages map[uint32]*TCPMessage // buffer of TCPMessages waiting to be send

	c_packets  chan *pcap.Packet
	c_messages chan *TCPMessage // Messages ready to be send to client

	sniffer *pcap.Pcap

	c_del_message chan *TCPMessage // Used for notifications about completed or expired messages

	device string // device to listen
	port int    // Port to listen
}

// RAWTCPListen creates a listener to capture traffic from RAW_SOCKET
func RAWTCPListen(device string, port int) (listener *RAWTCPListener) {
	listener = &RAWTCPListener{}

	listener.c_packets = make(chan *pcap.Packet, 100)
	listener.c_messages = make(chan *TCPMessage, 100)
	listener.c_del_message = make(chan *TCPMessage, 100)
	listener.messages = make(map[uint32]*TCPMessage)

	listener.device = device
	listener.port = port

	listener.startSniffer()

	go listener.listen()
	go listener.readRAWSocket()

	return
}

func (t *RAWTCPListener) listen() {
	for {
		select {
		// If message ready for deletion it means that its also complete or expired by timeout
		case message := <-t.c_del_message:
			t.c_messages <- message
			delete(t.messages, message.Ack)

		// We need to use channels to process each packet to avoid data races
		case packet := <-t.c_packets:
			t.processTCPPacket(packet)
		}
	}
}

func (t *RAWTCPListener) startSniffer() {
	devices, err := pcap.Findalldevs()

	if err != nil {
		log.Fatal("Error while getting device list", err)
	}

	networkInterface := ""

	for _, device := range devices {
		if device.Name == Settings.Device {
			networkInterface = device.Name
			break
		}
	}

	if networkInterface == "" {
		log.Fatal("Could not find network interface", Settings.Device)
	}

	h, err := pcap.Openlive(networkInterface, int32(4026), true, 0)
	h.Setfilter("tcp dst port " + string(t.port))

	if err != nil {
		log.Fatal("Error while trying to listen", err)
	}

	t.sniffer = h
}

func (t *RAWTCPListener) readRAWSocket() {
	for {
		// Note: ReadFrom receive messages without IP header
		pkt := t.sniffer.Next()

		if pkt == nil {
			continue
		}

		pkt.Decode()

		if len(pkt.Headers) < 2 {
			continue
		}

		switch pkt.Headers[1].(type) {
		case *pcap.Tcphdr:
			header := pkt.Headers[1].(*pcap.Tcphdr)
			port := int(header.DestPort)
			if port == t.port && (header.Flags & pcap.TCP_PSH) != 0 {
				t.c_packets <- pkt
			}
		}
	}
}

// Trying to add packet to existing message or creating new message
//
// For TCP message unique id is Acknowledgment number (see tcp_packet.go)
func (t *RAWTCPListener) processTCPPacket(packet *pcap.Packet) {
	var message *TCPMessage
	ack := packet.Headers[1].(*pcap.Tcphdr).Ack

	message, ok := t.messages[ack]

	if !ok {
		// We sending c_del_message channel, so message object can communicate with Listener and notify it if message completed
		message = NewTCPMessage(ack, t.c_del_message)
		t.messages[ack] = message
	}

	// Adding packet to message
	message.c_packets <- packet
}

// Receive TCP messages from the listener channel
func (t *RAWTCPListener) Receive() *TCPMessage {
	return <-t.c_messages
}
