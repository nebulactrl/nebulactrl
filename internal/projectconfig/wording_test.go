package projectconfig

import (
	"crypto/sha256"
	"encoding/hex"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// avoidedWords lists, as lower-case SHA-256 hex, the names of other platforms
// and the vocabulary NebulaCtrl does not use in nebula.toml, its docs, its
// schema or its messages. Hashes keep this file itself clean: add a word with
// `printf %s word | sha256sum`. A phrase of two words is hashed with one space.
var avoidedWords = map[string]bool{
	"ec5df277195755d82b8cbc3dedb4088ece38cece2ce935990373ef4dc4d83550": true,
	"9d3508a8484ee2520ca5fe0e52eab835774efa3fe533267e914eb00ad78d2579": true,
	"58465a6f6cb16f2719b55ba9e3a15b3864f2ec9d49c83fe6ec67579904cd798f": true,
	"e34d1c9011e06204d0540f17e6370d18d2163d69944a3b43b5c92d68d89096fe": true,
	"c903a60c5340f58bd6110efd0376fc9398591d25207518619db281b720cae4e4": true,
	"61a881df2f426222462b7da88aaa5496f7db09ac19aaa48fa3fd9d90acb0b9d8": true,
	"d38931a06bf8c8562a6e52dd22ca85f9e54946d6bfbd5cde96f5ffe84f882055": true,
	"ef765fe591697f81f37b24ac342704b2270c857b033bc52311ea818b1005c98b": true,
	"d1fccd652692a5b369118cdcf72e04959902709b024ae4a2b9e475e673d42475": true,
	"973207f486697ba9676d2ce1c368af82cca3532f07cb7773beb80662e7ba9e35": true,
	"33184048a238e8f599c1f5dbeebf7aa7dd4e234695340089553e25424d529e00": true,
	"f4debdb7ab7dd958e95cea076c7668310a131f9bf9c972fa934c5e55c5a97c0e": true,
	"05f4b4eb984387def95ba8e618f1c3eaaa5d8e0c531c521e680d16a887bf0d72": true,
	"b1ece0f3fb4f7be072543180ff03e21b7b094b69fb6a7f4d48cb170282cd967a": true,
	"e3bd85a7dcc38fdc155010c66f3ce0ba6be40b4630c68341617e33c893502f90": true,
	"3e2e3381da39c364e01229357da81d38c04ecc3d20abd3eef12758143ce2afd8": true,
	"de2f18bb7db111fd407aee70e81929afdede374faad33dd3ce867e700a9b3857": true,
	"3eb95cbef2662002788a3718fc4449dee594459c6e964ff68ee34435bd69461a": true,
	"1b86ab999b1f3d1ec79d60b903c598af09e5cb4a6b49720bffd41f67ebc69f4a": true,
	"5709ff28b655e82531c0fb3b0193b8ec9b1849128d8a0840c43d91ecb8e620d0": true,
	"6440718631802d31b3f96f7a804e12c92d5f20978468067e0351b380cb2bfd9c": true,
	"66c44929e67eb38c9c534bb7bcecb6d6b807b9432d86fcec35e13bf6fcfdffc9": true,
}

var wordRE = regexp.MustCompile(`[a-z0-9_]+`)

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// avoided returns the positions (1-based line) of avoided words and phrases in text.
func avoided(text string) []int {
	var lines []int
	for i, line := range strings.Split(text, "\n") {
		words := wordRE.FindAllString(strings.ToLower(line), -1)
		for j, w := range words {
			hit := avoidedWords[digest(w)]
			if !hit && j+1 < len(words) {
				hit = avoidedWords[digest(w+" "+words[j+1])]
			}
			if hit {
				lines = append(lines, i+1)
				break
			}
		}
	}
	return lines
}

func TestAvoidedWordsDetector(t *testing.T) {
	// Built from fragments so this file never spells an avoided word out.
	word := strings.Join([]string{"he", "ro", "ku"}, "")
	if got := avoided("deploy like\n" + strings.ToUpper(word) + " does\n"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("detector missed an avoided word: %v", got)
	}
	phrase := "app " + strings.Join([]string{"sp", "ec"}, "")
	if got := avoided("the " + phrase + " file"); len(got) != 1 {
		t.Fatalf("detector missed an avoided phrase: %v", got)
	}
	if got := avoided("services.web.health_check and nebula.toml"); len(got) != 0 {
		t.Fatalf("detector flagged clean text: %v", got)
	}
}

func TestNoAvoidedWording(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	add := func(dir string, ok func(path string) bool) {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d iofs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if ok(p) {
				files = append(files, p)
			}
			return nil
		})
	}
	all := func(string) bool { return true }
	add("docs/operations/project-config.md", all)
	add("docs/reference/nebula-toml.md", all)
	add("internal/projectconfig", func(p string) bool {
		return !strings.HasSuffix(p, "_test.go")
	})
	add("internal/configsync", func(p string) bool {
		return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
	})
	add("apps/web/src/features/project-config", func(p string) bool {
		return !strings.HasSuffix(p, ".test.ts")
	})
	add("apps/web/e2e/project-config.spec.ts", all)
	if len(files) < 10 {
		t.Fatalf("scanned only %d files: the paths moved", len(files))
	}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range avoided(string(b)) {
			t.Errorf("%s:%d uses a word nebula.toml does not use", p, line)
		}
	}
}

// The tests and fixtures of this package follow the same wording rules: the
// scan above skips *_test.go, so they are checked here (fixtures and the
// schema are already covered there).
func TestNoAvoidedWordingInTests(t *testing.T) {
	tests, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	files := append(tests, fixtures...)
	if len(files) < 5 {
		t.Fatalf("scanned only %d files: the paths moved", len(files))
	}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range avoided(string(b)) {
			t.Errorf("%s:%d uses a word nebula.toml does not use", p, line)
		}
	}
}
