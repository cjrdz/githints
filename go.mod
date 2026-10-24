module github.com/cjrdz/githints

// 1.25.5 is mcp-go's floor, but that patch release ships standard-library CVEs
// (net/url, crypto/tls, crypto/x509, net/http, encoding/asn1, net/textproto),
// all fixed by 1.25.13. Go 1.26.0–1.26.5 carry the same CVE classes, 1.26.7
// carries net/http, net/textproto, crypto/tls and os CVEs fixed by 1.26.9,
// and 1.26.9 carries a crypto/x509 CVE fixed by 1.26.10. CI builds from this
// directive, so pinning the floor at a vulnerable minimum would ship release
// binaries against a vulnerable stdlib.
go 1.26.10

require (
	github.com/mark3labs/mcp-go v1.3.0
	modernc.org/sqlite v1.60.1
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
