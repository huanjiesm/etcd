// Copyright 2015 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/etcd/Godeps/_workspace/src/golang.org/x/net/context"
)

type PrevExistType string

const (
	PrevIgnore  = PrevExistType("")
	PrevExist   = PrevExistType("true")
	PrevNoExist = PrevExistType("false")
)

var (
	defaultV2KeysPrefix = "/v2/keys"
)

// NewKeysAPI builds a KeysAPI that interacts with etcd's key-value
// API over HTTP.
func NewKeysAPI(c Client) KeysAPI {
	return NewKeysAPIWithPrefix(c, defaultV2KeysPrefix)
}

// NewKeysAPIWithPrefix acts like NewKeysAPI, but allows the caller
// to provide a custom base URL path. This should only be used in
// very rare cases.
func NewKeysAPIWithPrefix(c Client, p string) KeysAPI {
	return &httpKeysAPI{
		client: c,
		prefix: p,
	}
}

type KeysAPI interface {
	Set(ctx context.Context, key, value string, opts *SetOptions) (*Response, error)
	Create(ctx context.Context, key, value string) (*Response, error)
	Update(ctx context.Context, key, value string) (*Response, error)

	Delete(ctx context.Context, key string, opts *DeleteOptions) (*Response, error)

	Get(ctx context.Context, key string) (*Response, error)
	RGet(ctx context.Context, key string) (*Response, error)

	Watcher(key string, opts *WatcherOptions) Watcher
}

type WatcherOptions struct {
	// WaitIndex defines the index after-which the Watcher should
	// start emitting events. For example, if a value of 5 is
	// provided, the first event will have an index >= 6.
	//
	// Setting WaitIndex to 0 (default) means that the Watcher
	// should start watching for events starting at the current
	// index, whatever that may be.
	WaitIndex uint64

	// Recursive specifices whether or not the Watcher should emit
	// events that occur in children of the given keyspace. If set
	// to false (default), events will be limited to those that
	// occur for the exact key.
	Recursive bool
}

type SetOptions struct {
	// PrevValue specifies what the current value of the Node must
	// be in order for the Set operation to succeed.
	//
	// Leaving this field empty means that the caller wishes to
	// ignore the current value of the Node. This cannot be used
	// to compare the Node's current value to an empty string.
	PrevValue string

	// PrevIndex indicates what the current ModifiedIndex of the
	// Node must be in order for the Set operation to succeed.
	//
	// If PrevIndex is set to 0 (default), no comparison is made.
	PrevIndex uint64

	// PrevExist specifies whether the Node must currently exist
	// (PrevExist) or not (PrevNoExist). If the caller does not
	// care about existence, set PrevExist to PrevIgnore, or simply
	// leave it unset.
	PrevExist PrevExistType

	// TTL defines a period of time after-which the Node should
	// expire and no longer exist. Values <= 0 are ignored. Given
	// that the zero-value is ignored, TTL cannot be used to set
	// a TTL of 0.
	TTL time.Duration
}

type DeleteOptions struct {
	// PrevValue specifies what the current value of the Node must
	// be in order for the Delete operation to succeed.
	//
	// Leaving this field empty means that the caller wishes to
	// ignore the current value of the Node. This cannot be used
	// to compare the Node's current value to an empty string.
	PrevValue string

	// PrevIndex indicates what the current ModifiedIndex of the
	// Node must be in order for the Delete operation to succeed.
	//
	// If PrevIndex is set to 0 (default), no comparison is made.
	PrevIndex uint64

	// Recursive defines whether or not all children of the Node
	// should be deleted. If set to true, all children of the Node
	// identified by the given key will be deleted. If left unset
	// or explicitly set to false, only a single Node will be
	// deleted.
	Recursive bool
}

type Watcher interface {
	// Next blocks until an etcd event occurs, then returns a Response
	// represeting that event. The behavior of Next depends on the
	// WatcherOptions used to construct the Watcher. Next is designed to
	// be called repeatedly, each time blocking until a subsequent event
	// is available.
	//
	// If the provided context is cancelled, Next will return a non-nil
	// error. Any other failures encountered while waiting for the next
	// event (connection issues, deserialization failures, etc) will
	// also result in a non-nil error.
	Next(context.Context) (*Response, error)
}

type Response struct {
	Action   string `json:"action"`
	Node     *Node  `json:"node"`
	PrevNode *Node  `json:"prevNode"`
	Index    uint64
}

type Node struct {
	Key           string  `json:"key"`
	Value         string  `json:"value"`
	Nodes         []*Node `json:"nodes"`
	ModifiedIndex uint64  `json:"modifiedIndex"`
	CreatedIndex  uint64  `json:"createdIndex"`
}

func (n *Node) String() string {
	return fmt.Sprintf("{Key: %s, CreatedIndex: %d, ModifiedIndex: %d}", n.Key, n.CreatedIndex, n.ModifiedIndex)
}

type httpKeysAPI struct {
	client httpClient
	prefix string
}

func (k *httpKeysAPI) Set(ctx context.Context, key, val string, opts *SetOptions) (*Response, error) {
	act := &setAction{
		Prefix: k.prefix,
		Key:    key,
		Value:  val,
	}

	if opts != nil {
		act.PrevValue = opts.PrevValue
		act.PrevIndex = opts.PrevIndex
		act.PrevExist = opts.PrevExist
		act.TTL = opts.TTL
	}

	resp, body, err := k.client.Do(ctx, act)
	if err != nil {
		return nil, err
	}

	return unmarshalHTTPResponse(resp.StatusCode, resp.Header, body)
}

func (k *httpKeysAPI) Create(ctx context.Context, key, val string) (*Response, error) {
	return k.Set(ctx, key, val, &SetOptions{PrevExist: PrevNoExist})
}

func (k *httpKeysAPI) Update(ctx context.Context, key, val string) (*Response, error) {
	return k.Set(ctx, key, val, &SetOptions{PrevExist: PrevExist})
}

func (k *httpKeysAPI) Delete(ctx context.Context, key string, opts *DeleteOptions) (*Response, error) {
	act := &deleteAction{
		Prefix: k.prefix,
		Key:    key,
	}

	if opts != nil {
		act.PrevValue = opts.PrevValue
		act.PrevIndex = opts.PrevIndex
		act.Recursive = opts.Recursive
	}

	resp, body, err := k.client.Do(ctx, act)
	if err != nil {
		return nil, err
	}

	return unmarshalHTTPResponse(resp.StatusCode, resp.Header, body)
}

func (k *httpKeysAPI) Get(ctx context.Context, key string) (*Response, error) {
	get := &getAction{
		Prefix:    k.prefix,
		Key:       key,
		Recursive: false,
	}

	resp, body, err := k.client.Do(ctx, get)
	if err != nil {
		return nil, err
	}

	return unmarshalHTTPResponse(resp.StatusCode, resp.Header, body)
}

func (k *httpKeysAPI) RGet(ctx context.Context, key string) (*Response, error) {
	get := &getAction{
		Prefix:    k.prefix,
		Key:       key,
		Recursive: true,
	}

	resp, body, err := k.client.Do(ctx, get)
	if err != nil {
		return nil, err
	}

	return unmarshalHTTPResponse(resp.StatusCode, resp.Header, body)
}

func (k *httpKeysAPI) Watcher(key string, opts *WatcherOptions) Watcher {
	act := waitAction{
		Prefix: k.prefix,
		Key:    key,
	}

	if opts != nil {
		act.WaitIndex = opts.WaitIndex
		act.Recursive = opts.Recursive
	}

	return &httpWatcher{
		client:   k.client,
		nextWait: act,
	}
}

type httpWatcher struct {
	client   httpClient
	nextWait waitAction
}

func (hw *httpWatcher) Next(ctx context.Context) (*Response, error) {
	httpresp, body, err := hw.client.Do(ctx, &hw.nextWait)
	if err != nil {
		return nil, err
	}

	resp, err := unmarshalHTTPResponse(httpresp.StatusCode, httpresp.Header, body)
	if err != nil {
		return nil, err
	}

	hw.nextWait.WaitIndex = resp.Node.ModifiedIndex + 1
	return resp, nil
}

// v2KeysURL forms a URL representing the location of a key.
// The endpoint argument represents the base URL of an etcd
// server. The prefix is the path needed to route from the
// provided endpoint's path to the root of the keys API
// (typically "/v2/keys").
func v2KeysURL(ep url.URL, prefix, key string) *url.URL {
	ep.Path = path.Join(ep.Path, prefix, key)
	return &ep
}

type getAction struct {
	Prefix    string
	Key       string
	Recursive bool
}

func (g *getAction) HTTPRequest(ep url.URL) *http.Request {
	u := v2KeysURL(ep, g.Prefix, g.Key)

	params := u.Query()
	params.Set("recursive", strconv.FormatBool(g.Recursive))
	u.RawQuery = params.Encode()

	req, _ := http.NewRequest("GET", u.String(), nil)
	return req
}

type waitAction struct {
	Prefix    string
	Key       string
	WaitIndex uint64
	Recursive bool
}

func (w *waitAction) HTTPRequest(ep url.URL) *http.Request {
	u := v2KeysURL(ep, w.Prefix, w.Key)

	params := u.Query()
	params.Set("wait", "true")
	params.Set("waitIndex", strconv.FormatUint(w.WaitIndex, 10))
	params.Set("recursive", strconv.FormatBool(w.Recursive))
	u.RawQuery = params.Encode()

	req, _ := http.NewRequest("GET", u.String(), nil)
	return req
}

type setAction struct {
	Prefix    string
	Key       string
	Value     string
	PrevValue string
	PrevIndex uint64
	PrevExist PrevExistType
	TTL       time.Duration
}

func (a *setAction) HTTPRequest(ep url.URL) *http.Request {
	u := v2KeysURL(ep, a.Prefix, a.Key)

	params := u.Query()
	if a.PrevValue != "" {
		params.Set("prevValue", a.PrevValue)
	}
	if a.PrevIndex != 0 {
		params.Set("prevIndex", strconv.FormatUint(a.PrevIndex, 10))
	}
	if a.PrevExist != PrevIgnore {
		params.Set("prevExist", string(a.PrevExist))
	}
	u.RawQuery = params.Encode()

	form := url.Values{}
	form.Add("value", a.Value)
	if a.TTL > 0 {
		form.Add("ttl", strconv.FormatUint(uint64(a.TTL.Seconds()), 10))
	}
	body := strings.NewReader(form.Encode())

	req, _ := http.NewRequest("PUT", u.String(), body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req
}

type deleteAction struct {
	Prefix    string
	Key       string
	Value     string
	PrevValue string
	PrevIndex uint64
	Recursive bool
}

func (a *deleteAction) HTTPRequest(ep url.URL) *http.Request {
	u := v2KeysURL(ep, a.Prefix, a.Key)

	params := u.Query()
	if a.PrevValue != "" {
		params.Set("prevValue", a.PrevValue)
	}
	if a.PrevIndex != 0 {
		params.Set("prevIndex", strconv.FormatUint(a.PrevIndex, 10))
	}
	if a.Recursive {
		params.Set("recursive", "true")
	}
	u.RawQuery = params.Encode()

	req, _ := http.NewRequest("DELETE", u.String(), nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req
}

func unmarshalHTTPResponse(code int, header http.Header, body []byte) (res *Response, err error) {
	switch code {
	case http.StatusOK, http.StatusCreated:
		res, err = unmarshalSuccessfulResponse(header, body)
	default:
		err = unmarshalErrorResponse(code)
	}

	return
}

func unmarshalSuccessfulResponse(header http.Header, body []byte) (*Response, error) {
	var res Response
	err := json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if header.Get("X-Etcd-Index") != "" {
		res.Index, err = strconv.ParseUint(header.Get("X-Etcd-Index"), 10, 64)
	}
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func unmarshalErrorResponse(code int) error {
	switch code {
	case http.StatusNotFound:
		return ErrKeyNoExist
	case http.StatusPreconditionFailed:
		return ErrKeyExists
	case http.StatusInternalServerError:
		// this isn't necessarily true
		return ErrNoLeader
	case http.StatusGatewayTimeout:
		return ErrTimeout
	default:
	}

	return fmt.Errorf("unrecognized HTTP status code %d", code)
}
