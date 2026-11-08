package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"

	"golang.org/x/net/http2"
)

// StatsService uses standard unary gRPC over loopback h2c. Only the stable
// QueryStats protobuf fields are needed; reset is deliberately never sent.
var statsHTTP = &http.Client{Transport: &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}}}
var errUnimplemented = errors.New("gRPC StatsService not implemented")

func wireField(data []byte) (field int, wire int, value []byte, rest []byte, err error) {
	tag, n := binary.Uvarint(data)
	if n <= 0 {
		return 0, 0, nil, nil, fmt.Errorf("malformed protobuf tag")
	}
	data = data[n:]
	field = int(tag >> 3)
	wire = int(tag & 7)
	switch wire {
	case 0:
		_, n = binary.Uvarint(data)
		if n <= 0 {
			err = fmt.Errorf("malformed protobuf integer")
			return
		}
		value = data[:n]
		rest = data[n:]
	case 1:
		if len(data) < 8 {
			err = io.ErrUnexpectedEOF
			return
		}
		value = data[:8]
		rest = data[8:]
	case 2:
		length, k := binary.Uvarint(data)
		if k <= 0 || length > uint64(len(data)-k) {
			err = io.ErrUnexpectedEOF
			return
		}
		value = data[k : k+int(length)]
		rest = data[k+int(length):]
	case 5:
		if len(data) < 4 {
			err = io.ErrUnexpectedEOF
			return
		}
		value = data[:4]
		rest = data[4:]
	default:
		err = fmt.Errorf("unsupported protobuf wire type %d", wire)
	}
	return
}
func parseStats(data []byte) (map[string]int64, error) {
	out := map[string]int64{}
	for len(data) > 0 {
		f, w, v, rest, err := wireField(data)
		if err != nil {
			return nil, err
		}
		data = rest
		if f != 1 || w != 2 {
			continue
		}
		name := ""
		var count int64
		for len(v) > 0 {
			sf, sw, sv, srest, err := wireField(v)
			if err != nil {
				return nil, err
			}
			v = srest
			if sf == 1 && sw == 2 {
				name = string(sv)
			}
			if sf == 2 && sw == 0 {
				n, _ := binary.Uvarint(sv)
				if n > uint64(^uint64(0)>>1) {
					return nil, fmt.Errorf("negative or overflowing kernel counter")
				}
				count = int64(n)
			}
		}
		out[name] = count
	}
	return out, nil
}
func queryStatsService(ctx context.Context, addr, service string) (map[string]int64, error) {
	frame := []byte{0, 0, 0, 0, 0} // empty QueryStatsRequest: all counters, reset=false
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+addr+"/"+service+"/QueryStats", bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	resp, err := statsHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status")
	}
	if status == "12" {
		return nil, errUnimplemented
	}
	if resp.StatusCode != 200 || status != "0" {
		return nil, fmt.Errorf("stats RPC HTTP %d grpc-status %q", resp.StatusCode, status)
	}
	if len(raw) < 5 || raw[0] != 0 || int(binary.BigEndian.Uint32(raw[1:5])) != len(raw)-5 {
		return nil, fmt.Errorf("invalid gRPC stats frame")
	}
	return parseStats(raw[5:])
}
func readStats(ctx context.Context, addr, engine, prefix string) (int64, int64, error) {
	services := []string{"xray.app.stats.command.StatsService", "v2ray.core.app.stats.command.StatsService", "experimental.v2rayapi.StatsService"}
	if engine == "sing-box" {
		services = []string{"v2ray.core.app.stats.command.StatsService", "experimental.v2rayapi.StatsService"}
	}
	for _, service := range services {
		stats, err := queryStatsService(ctx, addr, service)
		if errors.Is(err, errUnimplemented) {
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		return stats[prefix+">>>traffic>>>uplink"], stats[prefix+">>>traffic>>>downlink"], nil
	}
	return 0, 0, fmt.Errorf("kernel does not expose a supported StatsService (sing-box needs with_v2ray_api)")
}
func loopbackPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	err = l.Close()
	return addr, err
}
func statsPort(addr string) int {
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	return n
}
