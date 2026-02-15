package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/net/proxy"
)

const (
	server         = "news.tcpreset.net:119"
	torProxy       = "127.0.0.1:9050"
	maxArticleSize = 64 * 1024 // 64 KB
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

	// Jetzt kommt NUR der entschlüsselte Text von GPG
	decrypted, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("error reading decrypted input: %v", err)
	}

	article := string(decrypted)

	if err := checkBlockedHeaders(article, config); err != nil {
		return err
	}

	if len(article) > maxArticleSize {
		return fmt.Errorf("article size exceeds %d KB", maxArticleSize/1024)
	}

	return sendRawArticle(article)
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

	// Read greeting
	_, err = bufReader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("error reading server greeting: %v", err)
	}

	// Send POST
	fmt.Fprint(conn, "POST\r\n")
	response, err := bufReader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("error sending POST command: %v", err)
	}

	if strings.HasPrefix(response, "340") {
		// Send article
		fmt.Fprint(conn, rawArticle)
		
		if !strings.HasSuffix(rawArticle, "\r\n") {
			fmt.Fprint(conn, "\r\n")
		}
		
		// Send end marker
		fmt.Fprint(conn, ".\r\n")
		
		// Read response
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

	// QUIT
	fmt.Fprint(conn, "QUIT\r\n")
	return nil
}
