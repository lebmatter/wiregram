// Package local runs models on this machine with go-llama, llama.cpp
// translated to pure Go, and downloads them from a fixed catalog.
package local

// Model is a catalog entry: a GGUF file on Hugging Face, pinned to a commit
// and checked against its SHA-256 once downloaded.
type Model struct {
	ID      string // what providers and bots name it by
	Name    string
	Repo    string
	Commit  string
	File    string
	Size    int64
	SHA256  string
	Context int // the context window it was trained for, in tokens
	License string
}

// URL is where the model downloads from.
func (m Model) URL() string {
	return "https://huggingface.co/" + m.Repo + "/resolve/" + m.Commit + "/" + m.File
}

// Catalog is every model wiregram offers to download: small instruct models
// that fit go-llama's 4 GiB of memory with their context.
var Catalog = []Model{
	{
		ID: "qwen2.5-0.5b", Name: "Qwen2.5 0.5B Instruct",
		Repo: "Qwen/Qwen2.5-0.5B-Instruct-GGUF", Commit: "9217f5db79a29953eb74d5343926648285ec7e67",
		File: "qwen2.5-0.5b-instruct-q4_k_m.gguf", Size: 491400032,
		SHA256:  "74a4da8c9fdbcd15bd1f6d01d621410d31c6fc00986f5eb687824e7b93d7a9db",
		Context: 32768, License: "Apache-2.0",
	},
	{
		ID: "qwen2.5-1.5b", Name: "Qwen2.5 1.5B Instruct",
		Repo: "Qwen/Qwen2.5-1.5B-Instruct-GGUF", Commit: "91cad51170dc346986eccefdc2dd33a9da36ead9",
		File: "qwen2.5-1.5b-instruct-q4_k_m.gguf", Size: 1117320736,
		SHA256:  "6a1a2eb6d15622bf3c96857206351ba97e1af16c30d7a74ee38970e434e9407e",
		Context: 32768, License: "Apache-2.0",
	},
	{
		ID: "llama3.2-1b", Name: "Llama 3.2 1B Instruct",
		Repo: "bartowski/Llama-3.2-1B-Instruct-GGUF", Commit: "067b946cf014b7c697f3654f621d577a3e3afd1c",
		File: "Llama-3.2-1B-Instruct-Q4_K_M.gguf", Size: 807694464,
		SHA256:  "6f85a640a97cf2bf5b8e764087b1e83da0fdb51d7c9fab7d0fece9385611df83",
		Context: 131072, License: "Llama 3.2",
	},
	{
		ID: "llama3.2-3b", Name: "Llama 3.2 3B Instruct",
		Repo: "bartowski/Llama-3.2-3B-Instruct-GGUF", Commit: "5ab33fa94d1d04e903623ae72c95d1696f09f9e8",
		File: "Llama-3.2-3B-Instruct-Q4_K_M.gguf", Size: 2019377696,
		SHA256:  "6c1a2b41161032677be168d354123594c0e6e67d2b9227c84f296ad037c728ff",
		Context: 131072, License: "Llama 3.2",
	},
	{
		ID: "gemma3-1b", Name: "Gemma 3 1B IT",
		Repo: "bartowski/google_gemma-3-1b-it-GGUF", Commit: "116f76234503685a98f572982177b11d44ec8ff1",
		File: "google_gemma-3-1b-it-Q4_K_M.gguf", Size: 806058496,
		SHA256:  "12bf0fff8815d5f73a3c9b586bd8fee8e7b248c935de70dec367679873d0f29d",
		Context: 32768, License: "Gemma",
	},
	{
		ID: "smollm2-1.7b", Name: "SmolLM2 1.7B Instruct",
		Repo: "HuggingFaceTB/SmolLM2-1.7B-Instruct-GGUF", Commit: "2d4a76a30b4af41ecd395c35725ac11688d4cfe4",
		File: "smollm2-1.7b-instruct-q4_k_m.gguf", Size: 1055609536,
		SHA256:  "decd2598bc2c8ed08c19adc3c8fdd461ee19ed5708679d1c54ef54a5a30d4f33",
		Context: 8192, License: "Apache-2.0",
	},
}

// Find is the catalog entry named id.
func Find(id string) (Model, bool) {
	for _, m := range Catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}
