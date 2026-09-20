package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAuthFileOwnRecord(t *testing.T) {
	storage, handled, errParse := ParseAuthFile([]byte(`{"type":"freebuff","auth_token":"aaaaaaaaaaaaaaaaaaaa","email":"me@example.com"}`), "/tmp/freebuff-me.json")
	if errParse != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, errParse)
	}
	if storage.Email != "me@example.com" || !storage.Valid() {
		t.Fatalf("storage = %+v", storage)
	}
	if storage.DisplayLabel() != "me@example.com" {
		t.Fatalf("label = %s", storage.DisplayLabel())
	}
}

func TestParseAuthFileCLICredentials(t *testing.T) {
	raw := []byte(`{"default":{"id":"user_1","name":"Zhang San","email":"z@example.com","authToken":"bbbbbbbbbbbbbbbbbbbb"}}`)
	storage, handled, errParse := ParseAuthFile(raw, "/tmp/credentials.json")
	if errParse != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, errParse)
	}
	if storage.AuthToken != "bbbbbbbbbbbbbbbbbbbb" || storage.Email != "z@example.com" {
		t.Fatalf("storage = %+v", storage)
	}
}

func TestParseAuthFileFlatTokenExport(t *testing.T) {
	storage, handled, _ := ParseAuthFile([]byte(`{"authToken":"cccccccccccccccccccc"}`), "/tmp/export.json")
	if !handled || storage.AuthToken != "cccccccccccccccccccc" {
		t.Fatalf("handled=%v storage=%+v", handled, storage)
	}
}

func TestParseAuthFileBareTokenOnlyForTokenFiles(t *testing.T) {
	token := []byte("dddddddddddddddddddd")
	if _, handled, _ := ParseAuthFile(token, "/tmp/token.txt"); !handled {
		t.Fatal("a .txt token file should be claimed")
	}
	if _, handled, _ := ParseAuthFile(token, "/tmp/notes.json"); handled {
		t.Fatal("a bare token in a .json file must not be claimed")
	}
}

func TestParseAuthFileIgnoresForeignDocuments(t *testing.T) {
	for _, raw := range []string{
		`{"access_token":"x","refresh_token":"y","type":"antigravity"}`,
		`{"installed":{"client_id":"abc"}}`,
		``,
	} {
		if _, handled, _ := ParseAuthFile([]byte(raw), "/tmp/other.json"); handled {
			t.Fatalf("claimed a foreign document: %s", raw)
		}
	}
}

func TestAuthIDIsStableAndCarriesNoToken(t *testing.T) {
	storage := Storage{AuthToken: "eeeeeeeeeeeeeeeeeeee"}
	first := storage.AuthID()
	if first != (Storage{AuthToken: "eeeeeeeeeeeeeeeeeeee"}).AuthID() {
		t.Fatal("auth id must be stable for the same token")
	}
	if strings.Contains(first, storage.AuthToken) {
		t.Fatal("auth id must not contain the token")
	}
	if (Storage{AuthToken: "ffffffffffffffffffff"}).AuthID() == first {
		t.Fatal("different tokens must produce different auth ids")
	}
}

func TestEncodeStorageRoundTrip(t *testing.T) {
	original := Storage{Type: providerKey, AuthToken: "gggggggggggggggggggg", Email: "a@b.c"}
	decoded, errDecode := decodeStorage(original.EncodeStorage())
	if errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if decoded.AuthToken != original.AuthToken || decoded.Email != original.Email {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestBuildAuthDataCarriesNoTokenInMetadata(t *testing.T) {
	storage := Storage{Type: providerKey, AuthToken: "hhhhhhhhhhhhhhhhhhhh", Email: "a@b.c"}
	auth := buildAuthData(storage, DefaultConfig())

	if auth.Provider != providerKey {
		t.Fatalf("provider = %s", auth.Provider)
	}
	if auth.Attributes["email"] != "a@b.c" {
		t.Fatalf("attributes = %v", auth.Attributes)
	}
	raw, errMarshal := json.Marshal(auth.Metadata)
	if errMarshal != nil {
		t.Fatalf("marshal metadata: %v", errMarshal)
	}
	if strings.Contains(string(raw), storage.AuthToken) {
		t.Fatal("auth metadata leaked the token")
	}
	if strings.Contains(string(auth.StorageJSON), "hhhhhhhhhhhhhhhhhhhh") == false {
		t.Fatal("storage json is expected to carry the token so the executor can use it")
	}
}
