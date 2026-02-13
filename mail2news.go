package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/net/proxy"
)

const (
	server         = "news.tcpreset.net:119"
	torProxy       = "127.0.0.1:9050"
	maxArticleSize = 64 * 1024 // 64 KB
	pgpPassphrase  = "your_passphrase"
	configFile     = "m2n.json"
)

type Config struct {
	BlockedHeaders []string `json:"blocked_headers"`
}

func main() {
	if err := processAndSendRawArticle(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig() (*Config, error) {
	config := &Config{
		BlockedHeaders: []string{},
	}

	file, err := os.Open(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return nil, fmt.Errorf("error opening config file: %v", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(config); err != nil {
		return nil, fmt.Errorf("error parsing config file: %v", err)
	}

	return config, nil
}

func checkBlockedHeaders(article string, config *Config) error {
	if config == nil || len(config.BlockedHeaders) == 0 {
		return nil
	}

	lines := strings.Split(article, "\n")
	
	for _, line := range lines {
		if line == "" || line == "\r" {
			break
		}

		lowerLine := strings.ToLower(line)
		for _, blocked := range config.BlockedHeaders {
			if strings.HasPrefix(lowerLine, strings.ToLower(blocked)+":") {
				return fmt.Errorf("article contains blocked header: %s", strings.Split(line, ":")[0])
			}
		}
	}

	return nil
}

func processAndSendRawArticle(reader io.Reader) error {
	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %v", err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("error reading input: %v", err)
	}

	pgpMessage := extractPGPMessage(string(data))
	if pgpMessage == "" {
		return fmt.Errorf("no PGP message found in email body")
	}

	decrypted, err := decryptWithGPG(pgpMessage)
	if err != nil {
		return fmt.Errorf("PGP decryption failed: %v", err)
	}

	if err := checkBlockedHeaders(decrypted, config); err != nil {
		return err
	}

	if len(decrypted) > maxArticleSize {
		return fmt.Errorf("article size exceeds %d KB", maxArticleSize/1024)
	}

	// Debug
	// fmt.Fprintf(os.Stderr, "--- DECRYPTED NEWS ARTICLE ---\n%s\n--- END ---\n", decrypted)

	return sendRawArticle(decrypted)
}

func extractPGPMessage(email string) string {
	lines := strings.Split(email, "\n")
	inPGP := false
	var pgp []string
	
	for _, line := range lines {
		if strings.Contains(line, "-----BEGIN PGP MESSAGE-----") {
			inPGP = true
			pgp = append(pgp, line)
			continue
		}
		
		if inPGP {
			pgp = append(pgp, line)
			if strings.Contains(line, "-----END PGP MESSAGE-----") {
				break
			}
		}
	}
	
	if !inPGP {
		return ""
	}
	
	return strings.Join(pgp, "\n")
}

func decryptWithGPG(encrypted string) (string, error) {
	cmd := exec.Command("gpg", "--decrypt", "--batch", "--quiet", 
		"--pinentry-mode", "loopback", "--passphrase-fd", "0")
	
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("error creating stdin pipe: %v", err)
	}
	
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("error starting gpg: %v", err)
	}
	
	stdin.Write([]byte(pgpPassphrase + "\n"))
	stdin.Write([]byte(encrypted))
	stdin.Close()
	
	err = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("gpg decryption failed: %v\nstderr: %s", err, stderr.String())
	}
	
	if stderr.Len() > 0 {
		fmt.Fprintf(os.Stderr, "GPG stderr: %s\n", stderr.String())
	}
	
	return stdout.String(), nil
}

func sendRawArticle(rawArticle string) error {
	dialer, err := proxy.SOCKS5("tcp", torProxy, nil, proxy.Direct)
	if err != nil {
		return fmt.Errorf("error creating SOCKS5 dialer: %v", err)
	}

	conn, err := dialer.Dial("tcp", server)
	if err != nil {
		return fmt.Errorf("error connecting to the server through Tor: %v", err)
	}
	defer conn.Close()

	bufReader := bufio.NewReader(conn)

	_, err = bufReader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("error reading server greeting: %v", err)
	}

	fmt.Fprint(conn, "POST\r\n")
	response, err := bufReader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("error sending POST command: %v", err)
	}

	if strings.HasPrefix(response, "340") {
		fmt.Fprint(conn, rawArticle)
		
		if !strings.HasSuffix(rawArticle, "\r\n") {
			fmt.Fprint(conn, "\r\n")
		}
		

		fmt.Fprint(conn, ".\r\n")
		
		response, err = bufReader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("error reading server response: %v", err)
		}
		
		fmt.Print(response)

		if !strings.HasPrefix(response, "240") {
			return fmt.Errorf("article transfer failed: %s", response)
		}
	} else {
		return fmt.Errorf("server did not accept POST command: %s", response)
	}

	fmt.Fprint(conn, "QUIT\r\n")
	return nil
}
