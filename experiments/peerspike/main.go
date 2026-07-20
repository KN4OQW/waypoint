// Command peerspike is a THROWAWAY latency spike for RFC-0016 (bus LAN peering):
// it measures round-trip latency + jitter for 20 ms-cadence ~55-byte voice-sized
// frames over (a) a persistent TCP+TLS connection and (b) the local mosquitto at
// QoS 0 and QoS 1, so the RFC's transport decision cites measured numbers, not
// estimates. It is not part of the product and ships no reusable code — see
// experiments/README.md.
//
// Method: the client stamps each frame with a sequence number and its own
// monotonic send time, the far side echoes the frame verbatim, and the client
// computes RTT against the SAME clock (no cross-host time sync needed). One-way
// latency is reported as RTT/2; jitter is the standard deviation of one-way
// latency, with p50/p95/p99 for the tail.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const frameSize = 55 // DMRD-sized voice frame

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: peerspike tcp-echo|tcp-client|mqtt-echo|mqtt-client [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	addr := fs.String("addr", ":9100", "tcp echo listen/connect address")
	broker := fs.String("broker", "127.0.0.1:1884", "mqtt broker host:port")
	qos := fs.Int("qos", 0, "mqtt QoS (0 or 1)")
	n := fs.Int("n", 500, "frames to send")
	cadence := fs.Duration("cadence", 20*time.Millisecond, "inter-frame cadence")
	label := fs.String("label", "", "row label for the results table")
	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "tcp-echo":
		tcpEcho(*addr)
	case "tcp-client":
		tcpClient(*addr, *n, *cadence, orLabel(*label, "TCP+TLS"))
	case "mqtt-echo":
		mqttEcho(*broker, *qos)
	case "mqtt-client":
		mqttClient(*broker, *qos, *n, *cadence, orLabel(*label, fmt.Sprintf("MQTT QoS %d", *qos)))
	default:
		fmt.Println("unknown command:", cmd)
		os.Exit(2)
	}
}

func orLabel(l, def string) string {
	if l != "" {
		return l
	}
	return def
}

// --- TCP + TLS ---------------------------------------------------------------

func selfSigned() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "peerspike"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, _ := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func tcpEcho(addr string) {
	cfg := &tls.Config{Certificates: []tls.Certificate{selfSigned()}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", addr, cfg)
	if err != nil {
		fatal(err)
	}
	fmt.Println("tcp-echo (TLS) listening on", addr)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			// length-prefixed frames: 2-byte big-endian length + payload, echoed verbatim.
			hdr := make([]byte, 2)
			for {
				if _, err := io.ReadFull(c, hdr); err != nil {
					return
				}
				l := int(binary.BigEndian.Uint16(hdr))
				buf := make([]byte, l)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				c.Write(hdr)
				c.Write(buf)
			}
		}(c)
	}
}

func tcpClient(addr string, n int, cadence time.Duration, label string) {
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err != nil {
		fatal(err)
	}
	defer c.Close()
	setNoDelay(c)

	lat := make([]float64, 0, n)
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		hdr := make([]byte, 2)
		for i := 0; i < n; i++ {
			if _, err := io.ReadFull(c, hdr); err != nil {
				break
			}
			l := int(binary.BigEndian.Uint16(hdr))
			buf := make([]byte, l)
			if _, err := io.ReadFull(c, buf); err != nil {
				break
			}
			sent := int64(binary.BigEndian.Uint64(buf[8:16]))
			rtt := time.Now().UnixNano() - sent
			mu.Lock()
			lat = append(lat, float64(rtt)/2/1e6) // one-way ms
			mu.Unlock()
		}
		close(done)
	}()

	frame := make([]byte, frameSize)
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(frameSize))
	tick := time.NewTicker(cadence)
	defer tick.Stop()
	for i := 0; i < n; i++ {
		<-tick.C
		binary.BigEndian.PutUint64(frame[0:8], uint64(i))
		binary.BigEndian.PutUint64(frame[8:16], uint64(time.Now().UnixNano()))
		c.Write(hdr)
		c.Write(frame)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	report(label, lat, n)
}

func setNoDelay(c net.Conn) {
	type nd interface{ NetConn() net.Conn }
	if t, ok := c.(nd); ok {
		if tc, ok := t.NetConn().(*net.TCPConn); ok {
			tc.SetNoDelay(true)
		}
	}
}

// --- MQTT --------------------------------------------------------------------

const (
	reqTopic = "waypoint/spike/req"
	repTopic = "waypoint/spike/rep"
)

func mqttEcho(broker string, qos int) {
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + broker).SetClientID("spike-echo").SetCleanSession(true)
	c := mqtt.NewClient(opts)
	if t := c.Connect(); t.Wait() && t.Error() != nil {
		fatal(t.Error())
	}
	fmt.Printf("mqtt-echo on %s subscribing %s -> %s (QoS %d)\n", broker, reqTopic, repTopic, qos)
	c.Subscribe(reqTopic, byte(qos), func(_ mqtt.Client, m mqtt.Message) {
		c.Publish(repTopic, byte(qos), false, m.Payload())
	})
	select {} // run until killed
}

func mqttClient(broker string, qos, n int, cadence time.Duration, label string) {
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + broker).SetClientID("spike-client").SetCleanSession(true).SetOrderMatters(false)
	c := mqtt.NewClient(opts)
	if t := c.Connect(); t.Wait() && t.Error() != nil {
		fatal(t.Error())
	}
	defer c.Disconnect(100)

	lat := make([]float64, 0, n)
	var mu sync.Mutex
	got := make(chan struct{}, n)
	c.Subscribe(repTopic, byte(qos), func(_ mqtt.Client, m mqtt.Message) {
		p := m.Payload()
		if len(p) < 16 {
			return
		}
		sent := int64(binary.BigEndian.Uint64(p[8:16]))
		rtt := time.Now().UnixNano() - sent
		mu.Lock()
		lat = append(lat, float64(rtt)/2/1e6)
		mu.Unlock()
		got <- struct{}{}
	}).Wait()

	frame := make([]byte, frameSize)
	tick := time.NewTicker(cadence)
	defer tick.Stop()
	for i := 0; i < n; i++ {
		<-tick.C
		binary.BigEndian.PutUint64(frame[0:8], uint64(i))
		binary.BigEndian.PutUint64(frame[8:16], uint64(time.Now().UnixNano()))
		c.Publish(reqTopic, byte(qos), false, append([]byte(nil), frame...))
	}
	deadline := time.After(3 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-got:
		case <-deadline:
			i = n
		}
	}
	report(label, lat, n)
}

// --- stats -------------------------------------------------------------------

func report(label string, lat []float64, sent int) {
	if len(lat) == 0 {
		fmt.Printf("%-14s | NO SAMPLES (sent %d)\n", label, sent)
		return
	}
	sort.Float64s(lat)
	mean := 0.0
	for _, v := range lat {
		mean += v
	}
	mean /= float64(len(lat))
	sd := 0.0
	for _, v := range lat {
		sd += (v - mean) * (v - mean)
	}
	sd = math.Sqrt(sd / float64(len(lat)))
	pct := func(p float64) float64 { return lat[int(math.Min(float64(len(lat)-1), p*float64(len(lat))))] }
	loss := 100 * float64(sent-len(lat)) / float64(sent)
	fmt.Printf("RESULT | %-12s | one-way ms: mean %.3f  median %.3f  jitter(sd) %.3f  p95 %.3f  p99 %.3f  max %.3f | loss %.1f%% (n=%d/%d)\n",
		label, mean, pct(0.50), sd, pct(0.95), pct(0.99), lat[len(lat)-1], loss, len(lat), sent)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "peerspike:", err)
	os.Exit(1)
}
