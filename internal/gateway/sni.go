package gateway

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// 手写解析 TLS ClientHello 只为取出 server_name 扩展；不做任何 TLS 终结。
// 读到的全部原始字节都保留，连接建立后原样写给后端，后端看到的是完整未改动的握手。

const (
	tlsRecordHandshake   = 0x16
	tlsHandshakeClientHe = 0x01
	tlsExtServerName     = 0x0000
	tlsSNIHostName       = 0x00

	// tlsMaxRecordLen TLS 1.2 record 明文上限 2^14；留 256 字节余量以兼容宽松实现。
	tlsMaxRecordLen = 16384 + 256
	// maxClientHelloBytes 完整 ClientHello（可能跨多个 record）的累计上限。
	maxClientHelloBytes = 64 << 10
	// tlsRecordHeaderLen record 头：type(1) version(2) length(2)。
	tlsRecordHeaderLen = 5
	// tlsHandshakeHeaderLen handshake 头：type(1) length(3)。
	tlsHandshakeHeaderLen = 4
)

var (
	// ErrNotTLS 首字节不是 TLS handshake record。
	ErrNotTLS = errors.New("not a TLS handshake")
	// ErrNotClientHello handshake 消息不是 ClientHello。
	ErrNotClientHello = errors.New("not a ClientHello")
	// ErrNoSNI ClientHello 没有 server_name 扩展。
	ErrNoSNI = errors.New("no SNI in ClientHello")
	// ErrClientHelloTooLarge 超出累计上限。
	ErrClientHelloTooLarge = errors.New("ClientHello too large")
	errTruncated           = errors.New("truncated ClientHello")
)

// ReadClientHello 从 r 读完整的 ClientHello（跨多个 TLS record 时拼接 handshake 片段），
// 返回读到的全部原始字节与 server_name（小写、去掉末尾点）。
// 调用方应事先在连接上设置读超时。
func ReadClientHello(r io.Reader) (raw []byte, serverName string, err error) {
	var hs []byte // 累积的 handshake 消息字节
	for {
		hdr := make([]byte, tlsRecordHeaderLen)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return raw, "", fmt.Errorf("read record header: %w", err)
		}
		raw = append(raw, hdr...)
		if hdr[0] != tlsRecordHandshake {
			return raw, "", ErrNotTLS
		}
		// 版本 major 必须是 3（SSL3.0/TLS1.x record 层）
		if hdr[1] != 0x03 {
			return raw, "", ErrNotTLS
		}
		n := int(hdr[3])<<8 | int(hdr[4])
		if n == 0 || n > tlsMaxRecordLen {
			return raw, "", fmt.Errorf("invalid record length %d", n)
		}
		if len(raw)+n > maxClientHelloBytes+tlsRecordHeaderLen {
			return raw, "", ErrClientHelloTooLarge
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return raw, "", fmt.Errorf("read record body: %w", err)
		}
		raw = append(raw, body...)
		hs = append(hs, body...)

		if len(hs) < tlsHandshakeHeaderLen {
			continue
		}
		if hs[0] != tlsHandshakeClientHe {
			return raw, "", ErrNotClientHello
		}
		hl := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
		if hl > maxClientHelloBytes {
			return raw, "", ErrClientHelloTooLarge
		}
		if len(hs) >= tlsHandshakeHeaderLen+hl {
			name, err := parseClientHelloSNI(hs[tlsHandshakeHeaderLen : tlsHandshakeHeaderLen+hl])
			return raw, name, err
		}
		// ClientHello 跨 record，继续读下一条
	}
}

// parseClientHelloSNI 解析 ClientHello 消息体（不含 4 字节 handshake 头），返回第一个 host_name。
func parseClientHelloSNI(b []byte) (string, error) {
	// client_version(2) + random(32)
	if len(b) < 34 {
		return "", errTruncated
	}
	b = b[34:]

	// session_id: 1 字节长度
	if len(b) < 1 {
		return "", errTruncated
	}
	l := int(b[0])
	b = b[1:]
	if len(b) < l {
		return "", errTruncated
	}
	b = b[l:]

	// cipher_suites: 2 字节长度
	if len(b) < 2 {
		return "", errTruncated
	}
	l = int(b[0])<<8 | int(b[1])
	b = b[2:]
	if len(b) < l {
		return "", errTruncated
	}
	b = b[l:]

	// compression_methods: 1 字节长度
	if len(b) < 1 {
		return "", errTruncated
	}
	l = int(b[0])
	b = b[1:]
	if len(b) < l {
		return "", errTruncated
	}
	b = b[l:]

	// extensions（可选）
	if len(b) == 0 {
		return "", ErrNoSNI
	}
	if len(b) < 2 {
		return "", errTruncated
	}
	l = int(b[0])<<8 | int(b[1])
	b = b[2:]
	if len(b) < l {
		return "", errTruncated
	}
	b = b[:l]

	for len(b) >= 4 {
		typ := int(b[0])<<8 | int(b[1])
		el := int(b[2])<<8 | int(b[3])
		b = b[4:]
		if len(b) < el {
			return "", errTruncated
		}
		ext := b[:el]
		b = b[el:]
		if typ != tlsExtServerName {
			continue
		}
		// ServerNameList: 2 字节长度，随后若干 (name_type(1), length(2), name)
		if len(ext) < 2 {
			return "", errTruncated
		}
		ll := int(ext[0])<<8 | int(ext[1])
		ext = ext[2:]
		if len(ext) < ll {
			return "", errTruncated
		}
		ext = ext[:ll]
		for len(ext) >= 3 {
			nt := ext[0]
			nl := int(ext[1])<<8 | int(ext[2])
			ext = ext[3:]
			if len(ext) < nl {
				return "", errTruncated
			}
			name := ext[:nl]
			ext = ext[nl:]
			if nt == tlsSNIHostName {
				return normalizeServerName(string(name))
			}
		}
		return "", ErrNoSNI
	}
	return "", ErrNoSNI
}

// normalizeServerName 小写、去末尾点，并做基本合法性检查（RFC 6066 禁止 IP 字面量与空名）。
func normalizeServerName(name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" {
		return "", ErrNoSNI
	}
	if len(name) > 253 {
		return "", fmt.Errorf("server name too long")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' && c != '.' {
			return "", fmt.Errorf("invalid character in server name")
		}
	}
	return name, nil
}
