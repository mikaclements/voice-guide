package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDocumentFlagNamesTheFileSentAsSessionContext(t *testing.T) {
	dir := t.TempDir()
	document, reference := filepath.Join(dir, "logbook.md"), filepath.Join(dir, "reference.txt")
	if err := os.WriteFile(document, []byte("Two seasons at the lamp."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reference, []byte("aiko-42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		SessionContext []struct{ Name, Content string } `json:"session_context"`
	}
	voice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode the start: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"s1","ticket":"t1"}`))
	}))
	defer voice.Close()

	if _, _, err := start(context.Background(), voice.URL, "key", "agent", reference, "logbook", document, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(sent.SessionContext) != 1 || sent.SessionContext[0].Name != "logbook" || sent.SessionContext[0].Content != "Two seasons at the lamp." {
		t.Errorf("sent %+v, want the file as the one document, under its name", sent.SessionContext)
	}
}

func TestTheOldResumeFlagIsGone(t *testing.T) {
	err := run(context.Background(), []string{"-resume", "cv.md"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "-resume") {
		t.Errorf("run = %v, want the flag refused as undefined", err)
	}
}
