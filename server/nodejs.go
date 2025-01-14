package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ije/gox/utils"
	"github.com/postui/postdb"
	"github.com/postui/postdb/q"
)

const (
	minNodejsVersion = 14
	nodejsLatestLTS  = "14.15.2"
	nodejsDistURL    = "https://nodejs.org/dist/"
	refreshDuration  = 10 * 60 // 10 minues
)

var builtInNodeModules = map[string]bool{
	"assert":              true,
	"async_hooks":         true,
	"child_process":       true,
	"cluster":             true,
	"buffer":              true,
	"console":             true,
	"constants":           true,
	"crypto":              true,
	"dgram":               true,
	"dns":                 true,
	"domain":              true,
	"events":              true,
	"fs":                  true,
	"http":                true,
	"http2":               true,
	"https":               true,
	"inspector":           true,
	"module":              true,
	"net":                 true,
	"os":                  true,
	"path":                true,
	"perf_hooks":          true,
	"process":             true,
	"punycode":            true,
	"querystring":         true,
	"readline":            true,
	"repl":                true,
	"stream":              true,
	"_stream_duplex":      true,
	"_stream_passthrough": true,
	"_stream_readable":    true,
	"_stream_transform":   true,
	"_stream_writable":    true,
	"string_decoder":      true,
	"sys":                 true,
	"timers":              true,
	"tls":                 true,
	"trace_events":        true,
	"tty":                 true,
	"url":                 true,
	"util":                true,
	"v8":                  true,
	"vm":                  true,
	"worker_threads":      true,
	"zlib":                true,
}

// status: https://deno.land/std/node
var denoStdNodeModules = map[string]bool{
	"assert": true,
	// "async_hooks":         true,
	// "child_process":       true,
	// "cluster":             true,
	"buffer": true,
	// "console":             true,
	"constants": true,
	"crypto":    true,
	// "dgram":               true,
	// "dns":                 true,
	// "domain":              true,
	// "events":              true,  // disable for https://github.com/postui/esm.sh/issues/27
	"fs": true,
	// "http":                true,
	// "http2":               true,
	// "https":               true,
	// "inspector":           true,
	"module": true,
	// "net":                 true,
	"os":   true,
	"path": true,
	// "perf_hooks":          true,
	"process": true,
	// "punycode":            true,
	"querystring": true,
	// "readline":            true,
	// "repl":                true,
	"stream": true,
	// "_stream_duplex":      true,
	// "_stream_passthrough": true,
	// "_stream_readable":    true,
	// "_stream_transform":   true,
	// "_stream_writable":    true,
	"string_decoder": true,
	// "sys":            true,
	"timers": true,
	// "tls":            true,
	// "trace_events":   true,
	"tty":  true,
	"url":  true,
	"util": true,
	// "v8":             true,
	// "vm":             true,
	// "worker_threads": true,
	// "zlib":           true,
}

// copy from https://github.com/webpack/webpack/blob/master/lib/ModuleNotFoundError.js#L13
var polyfilledBuiltInNodeModules = map[string]string{
	"assert":              "assert",
	"buffer":              "buffer",
	"console":             "console-browserify",
	"constants":           "constants-browserify",
	"crypto":              "crypto-browserify",
	"domain":              "domain-browser",
	"events":              "events",
	"http":                "stream-http",
	"https":               "https-browserify",
	"os":                  "os-browserify/browser",
	"path":                "path-browserify",
	"punycode":            "punycode",
	"process":             "process/browser",
	"querystring":         "querystring-es3",
	"stream":              "stream-browserify",
	"_stream_duplex":      "readable-stream/duplex",
	"_stream_passthrough": "readable-stream/passthrough",
	"_stream_readable":    "readable-stream/readable",
	"_stream_transform":   "readable-stream/transform",
	"_stream_writable":    "readable-stream/writable",
	"string_decoder":      "string_decoder",
	"sys":                 "util",
	"timers":              "timers-browserify",
	"tty":                 "tty-browserify",
	"url":                 "url",
	"util":                "util",
	"vm":                  "vm-browserify",
	"zlib":                "browserify-zlib",
}

// NpmPackageRecords defines version records of a npm package
type NpmPackageRecords struct {
	DistTags map[string]string     `json:"dist-tags"`
	Versions map[string]NpmPackage `json:"versions"`
}

// NpmPackage defines the package of npm
type NpmPackage struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Main             string            `json:"main,omitempty"`
	Module           string            `json:"module,omitempty"`
	Types            string            `json:"types,omitempty"`
	Typings          string            `json:"typings,omitempty"`
	Dependencies     map[string]string `json:"dependencies,omitempty"`
	PeerDependencies map[string]string `json:"peerDependencies,omitempty"`
}

// NodeEnv defines the nodejs env
type NodeEnv struct {
	version     string
	npmRegistry string
}

func checkNodeEnv() (env *NodeEnv, err error) {
	env = &NodeEnv{
		npmRegistry: "https://registry.npmjs.org/",
	}

	var installed bool
CheckNodejs:
	version, major, err := getNodejsVersion()
	if err != nil || major < minNodejsVersion {
		PATH := os.Getenv("PATH")
		nodeBinDir := "/usr/local/nodejs/bin"
		if !strings.Contains(PATH, nodeBinDir) {
			os.Setenv("PATH", fmt.Sprintf("%s%c%s", nodeBinDir, os.PathListSeparator, PATH))
			goto CheckNodejs
		} else if !installed {
			err = os.RemoveAll("/usr/local/nodejs")
			if err != nil {
				return
			}
			err = installNodejs("/usr/local/nodejs", nodejsLatestLTS)
			if err != nil {
				return
			}
			log.Infof("nodejs %s installed", nodejsLatestLTS)
			installed = true
			goto CheckNodejs
		} else {
			if err == nil {
				err = fmt.Errorf("bad nodejs version %s need %d+", env.version, minNodejsVersion)
			}
			return
		}
	}
	env.version = version

	output, err := exec.Command("npm", "config", "get", "registry").CombinedOutput()
	if err == nil {
		env.npmRegistry = strings.TrimRight(strings.TrimSpace(string(output)), "/") + "/"
	}

CheckYarn:
	output, err = exec.Command("yarn", "-v").CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			output, err = exec.Command("npm", "install", "yarn", "-g").CombinedOutput()
			if err != nil {
				err = errors.New("install yarn: " + strings.TrimSpace(string(output)))
				return
			}
			goto CheckYarn
		}
		err = errors.New("bad yarn version")
	}
	return
}

func (env *NodeEnv) getPackageInfo(name string, version string) (info NpmPackage, err error) {
	if !strings.HasPrefix(name, "@") {
		name, _ = utils.SplitByFirstByte(name, '/')
	}
	key := name + "/" + version
	isFullVersion := regFullVersion.MatchString(version)
	p, err := db.Get(q.Alias(key), q.K("package"))
	if err == nil {
		if !isFullVersion && int64(p.Crtime)+refreshDuration < time.Now().Unix() {
			_, err = db.Delete(q.Alias(key))
		} else if json.Unmarshal(p.KV.Get("package"), &info) == nil {
			return
		}
	}
	if err != nil && err != postdb.ErrNotFound {
		return
	}

	resp, err := http.Get(env.npmRegistry + name)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 || resp.StatusCode == 401 {
		err = fmt.Errorf("npm: package '%s' not found", name)
		return
	} else if resp.StatusCode != 200 {
		ret, _ := ioutil.ReadAll(resp.Body)
		err = fmt.Errorf("npm: can't get metadata of package '%s' (%s: %s)", name, resp.Status, string(ret))
		return
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err == io.EOF {
		err = nil
	}
	if err != nil {
		return
	}

	var h NpmPackageRecords
	err = json.Unmarshal(data, &h)
	if err != nil {
		return
	}

	if isFullVersion {
		info = h.Versions[version]
	} else {
		distVersion, ok := h.DistTags[version]
		if ok {
			info = h.Versions[distVersion]
		} else {
			var majorVerions versionSlice
			for key := range h.Versions {
				if regFullVersion.MatchString(key) && strings.HasPrefix(key, version+".") {
					majorVerions = append(majorVerions, key)
				}
			}
			if l := len(majorVerions); l > 0 {
				if l > 1 {
					sort.Sort(majorVerions)
				}
				info = h.Versions[majorVerions[0]]
			}
		}
	}

	if info.Version == "" {
		err = fmt.Errorf("npm: version '%s' not found", version)
		return
	}

	db.Put(q.Alias(key), q.Tags("package"), q.KV{"package": utils.MustEncodeJSON(info)})
	return
}

func getNodejsVersion() (version string, major int, err error) {
	output, err := exec.Command("node", "--version").CombinedOutput()
	if err != nil {
		return
	}

	version = strings.TrimPrefix(strings.TrimSpace(string(output)), "v")
	s, _ := utils.SplitByFirstByte(version, '.')
	major, err = strconv.Atoi(s)
	return
}

func installNodejs(dir string, version string) (err error) {
	dlURL := fmt.Sprintf("%sv%s/node-v%s-%s-x64.tar.xz", nodejsDistURL, version, version, runtime.GOOS)
	log.Debugf("downloading %s", dlURL)
	resp, err := http.Get(dlURL)
	if err != nil {
		err = fmt.Errorf("download nodejs: %v", err)
		return
	}
	defer resp.Body.Close()

	savePath := path.Join(os.TempDir(), path.Base(dlURL))
	f, err := os.Create(savePath)
	if err != nil {
		return
	}
	io.Copy(f, resp.Body)
	f.Close()

	cmd := exec.Command("tar", "-xJf", path.Base(dlURL))
	cmd.Dir = os.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			err = errors.New(string(output))
		}
		return
	}

	cmd = exec.Command("mv", "-f", strings.TrimSuffix(path.Base(dlURL), ".tar.xz"), dir)
	cmd.Dir = os.TempDir()
	output, err = cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			err = errors.New(string(output))
		}
	}
	return
}

func yarnAdd(packages ...string) (err error) {
	if len(packages) > 0 {
		start := time.Now()
		args := append([]string{"add", "--silent", "--no-progress", "--ignore-scripts"}, packages...)
		output, err := exec.Command("yarn", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("yarn: %s", string(output))
		}
		log.Debug("yarn add", strings.Join(packages, " "), "in", time.Now().Sub(start))
	}
	return
}
