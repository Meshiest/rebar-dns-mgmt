package main

/* These are needed for database access
 *   "database/sql"
 *    _ "github.com/lib/pq"
 *
 * These are needed for consul api accces
 *    "github.com/hashicorp/consul/api"
 */

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/digitalrebar/gcfg"
)

// For NSUPDATE, Dns.Server is ip of server to update
// For PDNS, Dns.Server to access (localhost)
// For BIND, Dns.Server name (FQDN of DNS server)
type Config struct {
	Dns struct {
		Type     string
		Hostname string
		Password string
		Port     int
		Server   string
	}
	Network struct {
		Port     int
		Username string
		Password string
	}
}

var auth_mode, config_path, key_pem, cert_pem, base_cert_pem, data_dir, backingStore string

func init() {
	flag.StringVar(&config_path, "config_path", "/etc/dns-mgmt.conf", "Path to config file")
	flag.StringVar(&key_pem, "key_pem", "/etc/dns-mgmt-https-key.pem", "Path to config file")
	flag.StringVar(&cert_pem, "cert_pem", "/etc/dns-mgmt-https-cert.pem", "Path to config file")
	flag.StringVar(&base_cert_pem, "base_cert_pem", "/etc/dns-mgmt-base-cert.pem", "Path to verifying certificate")
	flag.StringVar(&auth_mode, "auth_mode", "BASIC", "AUth mode to use: BASIC or KEY")
	flag.StringVar(&data_dir, "data_dir", "/var/cache/rebar-dns-mgmt", "Path to store data")
	flag.StringVar(&backingStore, "backing_store", "file", "Backing store to use. Either 'consul' or 'file'")
}

func main() {
	flag.Parse()

	var cfg Config
	err := gcfg.ReadFileInto(&cfg, config_path)
	if err != nil {
		log.Fatal(err)
	}

	var be dns_backend_point

	if cfg.Dns.Type == "BIND" {
		be = NewBindDnsInstance(cfg.Dns.Server)
	} else if cfg.Dns.Type == "POWERDNS" {
		base := fmt.Sprintf("http://%s:%d/servers/%s", cfg.Dns.Hostname, cfg.Dns.Port, cfg.Dns.Server)
		be = &PowerDnsInstance{
			UrlBase:     base,
			AccessToken: cfg.Dns.Password,
		}
	} else if cfg.Dns.Type == "NSUPDATE" {
		be = NewNsupdateDnsInstance(cfg.Dns.Server)
	} else {
		log.Fatal("Failed to find type")
	}
	var bs LoadSaver
	switch backingStore {
	case "file":
		bs, err = NewFileStore(data_dir + "/database.json")
	case "consul":
		bs, err = NewConsulStore(data_dir)
	default:
		log.Fatalf("Unknown backing store type %s", backingStore)
	}

	fe := NewFrontend(&be, bs)

	fe.load_data()

	api := rest.NewApi()
	if auth_mode == "BASIC" {
		api.Use(&rest.AuthBasicMiddleware{
			Realm: "test zone",
			Authenticator: func(userId string, password string) bool {
				if userId == cfg.Network.Username && password == cfg.Network.Password {
					return true
				}
				return false
			},
		})
	}
	api.Use(rest.DefaultDevStack...)
	router, err := rest.MakeRouter(
		rest.Get("/zones", fe.GetAllZones),
		rest.Get("/zones/#id", fe.GetZone),
		&rest.Route{"PATCH", "/zones/#id", fe.PatchZone},
	)
	if err != nil {
		log.Fatal(err)
	}
	api.SetApp(router)

	connStr := fmt.Sprintf(":%d", cfg.Network.Port)
	log.Println("Using", connStr)

	server := &http.Server{
		Addr:    connStr,
		Handler: api.MakeHandler(),
	}

	if auth_mode == "KEY" {
		caCert, err := ioutil.ReadFile(base_cert_pem)
		if err != nil {
			log.Fatal(err)
		}
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		// Setup HTTPS client
		tlsConfig := &tls.Config{
			ClientCAs: caCertPool,
			// NoClientCert
			// RequestClientCert
			// RequireAnyClientCert
			// VerifyClientCertIfGiven
			// RequireAndVerifyClientCert
			ClientAuth: tls.RequireAndVerifyClientCert,
		}
		tlsConfig.BuildNameToCertificate()
		server.TLSConfig = tlsConfig
	}

	log.Fatal(server.ListenAndServeTLS(cert_pem, key_pem))
}
