package main

import (
	"fmt"
	"os"
	"strconv"
)

// The init container uses the same private image to create gateway-only
// credentials in a new named volume. It never reads any existing gateway path.
func writeGatewayConfig(path string) error {
	token := os.Getenv("SIM_WEBHOOK_TOKEN")
	if len(token) < 32 {
		return fmt.Errorf("SIM_WEBHOOK_TOKEN must contain at least 32 characters")
	}
	content := `http_addr = "0.0.0.0:8080"
addr = "0.0.0.0"
udp_port = 25060
rtp_bind_ip = "0.0.0.0"
rtp_start_port = 24000
rtp_end_port = 24019
codecs = ["pcmu"]
accept_timeout = "15s"
log_level = "warn"
auto_refer = false
enable_options_response = false
graceful_shutdown = true
graceful_shutdown_timeout = 5
[recording]
enabled = false
[handler]
type = "webhook"
url = "http://simulator:18865/active-call/incoming"
method = "POST"
headers = [["Authorization", ` + strconv.Quote("Bearer "+token) + "]]\n"
	// Re-rendering on an explicitly restarted init service rotates only this
	// project's isolated gateway secret; rename avoids partial configurations.
	f, err := os.CreateTemp("/gateway", ".config-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString(content)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
