package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
)

func main() {
	reqBody := []byte(`{"model":"opencode/deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	
	req, _ := http.NewRequest("POST", "http://localhost:20128/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	// For testing, make sure your dev server is running and auth is bypassed/handled
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Request error: %v\n", err)
		return
	}
	defer resp.Body.Close()
	
	fmt.Printf("Status: %d\n", resp.StatusCode)
	
	contentType := resp.Header.Get("Content-Type")
	fmt.Printf("Content-Type: %s\n", contentType)
	
	if len(contentType) < 17 || contentType[:17] != "text/event-stream" {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Body: %s\n", string(body))
		return
	}
	
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fmt.Printf("CHUNK: %s\n", scanner.Text())
	}
}
