// Package cast discovers DLNA media renderers on the local network and drives
// their AVTransport service. It talks plain UPnP SOAP so the TV can pull media
// straight from a LAN URL served by the host application.
package cast

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	mediaRendererType = "urn:schemas-upnp-org:device:MediaRenderer:1"
	avTransportType   = "urn:schemas-upnp-org:service:AVTransport:1"
	ssdpAddress       = "239.255.255.250:1900"
)

// Device is a discovered DLNA media renderer.
type Device struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Model               string `json:"model,omitempty"`
	Manufacturer        string `json:"manufacturer,omitempty"`
	Host                string `json:"host"`
	Location            string `json:"location"`
	AVTransportURL      string `json:"av_transport_url"`
	RenderingControlURL string `json:"rendering_control_url,omitempty"`
}

// Discover sends an SSDP M-SEARCH for MediaRenderers and returns the ones whose
// device description exposes an AVTransport service. Duplicate UDNs that answer
// on several ports or interfaces collapse into one device.
func Discover(ctx context.Context, timeout time.Duration) ([]Device, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("open SSDP socket: %w", err)
	}
	defer conn.Close()

	request := strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + ssdpAddress,
		`MAN: "ssdp:discover"`,
		"MX: 3",
		"ST: " + mediaRendererType,
		"", "",
	}, "\r\n")
	target, err := net.ResolveUDPAddr("udp4", ssdpAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve SSDP address: %w", err)
	}

	// Most routers drop the first multicast datagram; keep sending until the
	// deadline so every renderer gets a chance to answer.
	go func() {
		ticker := time.NewTicker(700 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := conn.WriteTo([]byte(request), target); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	locations := map[string]bool{}
	buffer := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			break
		}
		if location := ssdpLocation(buffer[:n]); location != "" {
			locations[location] = true
		}
	}

	if len(locations) == 0 {
		return nil, nil
	}
	// The description fetch needs its own budget: the SSDP wait above usually
	// consumes the whole discovery timeout.
	fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancelFetch()
	return fetchDevices(fetchCtx, locations)
}

type deviceDescription struct {
	XMLName xml.Name `xml:"root"`
	URLBase string   `xml:"URLBase"`
	Device  struct {
		DeviceType       string `xml:"deviceType"`
		FriendlyName     string `xml:"friendlyName"`
		Manufacturer     string `xml:"manufacturer"`
		ModelName        string `xml:"modelName"`
		ModelDescription string `xml:"modelDescription"`
		UDN              string `xml:"UDN"`
		ServiceList      struct {
			Services []struct {
				ServiceType string `xml:"serviceType"`
				ControlURL  string `xml:"controlURL"`
			} `xml:"service"`
		} `xml:"serviceList"`
	} `xml:"device"`
}

func fetchDevices(ctx context.Context, locations map[string]bool) ([]Device, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	results := make(chan *Device, len(locations))
	for location := range locations {
		location := location
		go func() {
			device, err := fetchDevice(ctx, client, location)
			if err != nil {
				results <- nil
				return
			}
			results <- device
		}()
	}

	byID := map[string]Device{}
	order := []string{}
	for range locations {
		device := <-results
		if device == nil {
			continue
		}
		if existing, ok := byID[device.ID]; ok {
			// Prefer the endpoint on the port that answered first; keep the
			// name/model from whichever description carried more detail.
			if len(device.Name) > len(existing.Name) {
				existing.Name = device.Name
			}
			if existing.Model == "" {
				existing.Model = device.Model
			}
			byID[device.ID] = existing
			continue
		}
		byID[device.ID] = *device
		order = append(order, device.ID)
	}

	devices := make([]Device, 0, len(byID))
	for _, id := range order {
		devices = append(devices, byID[id])
	}
	// Bilibili's TV app first, then everything else alphabetically.
	sort.SliceStable(devices, func(i, j int) bool {
		bi := strings.Contains(strings.ToLower(devices[i].Model+" "+devices[i].Name), "bilibili") ||
			strings.Contains(devices[i].Manufacturer, "Bilibili") ||
			strings.Contains(devices[i].Name, "小电视")
		bj := strings.Contains(strings.ToLower(devices[j].Model+" "+devices[j].Name), "bilibili") ||
			strings.Contains(devices[j].Manufacturer, "Bilibili") ||
			strings.Contains(devices[j].Name, "小电视")
		if bi != bj {
			return bi
		}
		return devices[i].Name < devices[j].Name
	})
	return devices, nil
}

func fetchDevice(ctx context.Context, client *http.Client, location string) (*Device, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("description %s returned %s", location, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil {
		return nil, err
	}
	return parseDescription(location, data)
}

func parseDescription(location string, data []byte) (*Device, error) {
	var description deviceDescription
	if err := xml.Unmarshal(data, &description); err != nil {
		return nil, err
	}
	info := description.Device
	if !strings.Contains(info.DeviceType, "MediaRenderer") {
		return nil, fmt.Errorf("device %q is not a media renderer", info.FriendlyName)
	}

	base := location
	if strings.TrimSpace(description.URLBase) != "" {
		base = strings.TrimSpace(description.URLBase)
	}
	controlURL := ""
	renderingURL := ""
	for _, service := range info.ServiceList.Services {
		switch {
		case strings.HasPrefix(service.ServiceType, avTransportType):
			if controlURL == "" {
				controlURL = resolveURL(base, location, service.ControlURL)
			}
		case strings.HasPrefix(service.ServiceType, "urn:schemas-upnp-org:service:RenderingControl:1"):
			if renderingURL == "" {
				renderingURL = resolveURL(base, location, service.ControlURL)
			}
		}
	}
	if controlURL == "" {
		return nil, fmt.Errorf("device %q has no AVTransport service", info.FriendlyName)
	}

	parsed, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(info.UDN)
	if id == "" {
		id = location
	}
	name := strings.TrimSpace(info.FriendlyName)
	if name == "" {
		name = parsed.Host
	}
	return &Device{
		ID:                  id,
		Name:                name,
		Model:               firstNonEmpty(info.ModelDescription, info.ModelName),
		Manufacturer:        strings.TrimSpace(info.Manufacturer),
		Host:                parsed.Hostname(),
		Location:            location,
		AVTransportURL:      controlURL,
		RenderingControlURL: renderingURL,
	}, nil
}

func resolveURL(base, location, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	relative, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if relative.IsAbs() {
		return relative.String()
	}
	resolvedBase, err := url.Parse(base)
	if err != nil {
		resolvedBase, err = url.Parse(location)
		if err != nil {
			return ""
		}
	}
	if !strings.HasSuffix(resolvedBase.Path, "/") && resolvedBase.Path != "" {
		resolvedBase.Path += "/"
	}
	return resolvedBase.ResolveReference(relative).String()
}

func ssdpLocation(message []byte) string {
	lines := strings.Split(string(message), "\n")
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "location") {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// VideoMetadata builds the DIDL-Lite document DLNA renderers expect alongside
// a media URL. A blank title falls back to a generic label.
func VideoMetadata(title, mediaURL, mimeType string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "视频"
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/vnd.apple.mpegurl"
	}
	var builder strings.Builder
	builder.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" `)
	builder.WriteString(`xmlns:dc="http://purl.org/dc/elements/1.1/" `)
	builder.WriteString(`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
	builder.WriteString(`<item id="0" parentID="-1" restricted="1">`)
	builder.WriteString("<dc:title>")
	appendEscaped(&builder, title)
	builder.WriteString("</dc:title>")
	builder.WriteString("<upnp:class>object.item.videoItem</upnp:class>")
	builder.WriteString(`<res protocolInfo="http-get:*:`)
	appendEscaped(&builder, mimeType)
	builder.WriteString(`:*">`)
	appendEscaped(&builder, mediaURL)
	builder.WriteString("</res></item></DIDL-Lite>")
	return builder.String()
}

// SetAVTransportURI points the renderer at a media URL and attaches metadata.
func SetAVTransportURI(ctx context.Context, controlURL, mediaURL, metadata string) error {
	var body strings.Builder
	body.WriteString("<InstanceID>0</InstanceID><CurrentURI>")
	appendEscaped(&body, mediaURL)
	body.WriteString("</CurrentURI><CurrentURIMetaData>")
	appendEscaped(&body, metadata)
	body.WriteString("</CurrentURIMetaData>")
	_, err := soapAction(ctx, controlURL, avTransportType, "SetAVTransportURI", body.String())
	return err
}

// Play starts playback at normal speed.
func Play(ctx context.Context, controlURL string) error {
	_, err := soapAction(ctx, controlURL, avTransportType, "Play",
		"<InstanceID>0</InstanceID><Speed>1</Speed>")
	return err
}

// Pause pauses playback when the renderer supports it.
func Pause(ctx context.Context, controlURL string) error {
	_, err := soapAction(ctx, controlURL, avTransportType, "Pause", "<InstanceID>0</InstanceID>")
	return err
}

// Stop halts playback.
func Stop(ctx context.Context, controlURL string) error {
	_, err := soapAction(ctx, controlURL, avTransportType, "Stop", "<InstanceID>0</InstanceID>")
	return err
}

// Seek moves playback to an absolute position.
func Seek(ctx context.Context, controlURL string, position time.Duration) error {
	if position < 0 {
		position = 0
	}
	if _, err := soapAction(ctx, controlURL, avTransportType, "Seek",
		"<InstanceID>0</InstanceID><Unit>REL_TIME</Unit><Target>"+formatSeekTime(position)+"</Target>"); err != nil {
		return err
	}
	return nil
}

// TransportInfo returns the renderer's transport state (PLAYING, STOPPED, …)
// and status (OK or ERROR_OCCURRED).
func TransportInfo(ctx context.Context, controlURL string) (string, string, error) {
	payload, err := soapAction(ctx, controlURL, avTransportType, "GetTransportInfo", "<InstanceID>0</InstanceID>")
	if err != nil {
		return "", "", err
	}
	state := xmlField(payload, "CurrentTransportState")
	status := xmlField(payload, "CurrentTransportStatus")
	return state, status, nil
}

func soapAction(ctx context.Context, controlURL, serviceType, action, body string) ([]byte, error) {
	if strings.TrimSpace(controlURL) == "" {
		return nil, fmt.Errorf("renderer control URL is empty")
	}
	var envelope strings.Builder
	envelope.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	envelope.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" `)
	envelope.WriteString(`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	envelope.WriteString(`<u:` + action + ` xmlns:u="` + serviceType + `">`)
	envelope.WriteString(body)
	envelope.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, bytes.NewReader([]byte(envelope.String())))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	request.Header.Set("SOAPAction", `"`+serviceType+`#`+action+`"`)
	request.Header.Set("User-Agent", "BTB/1.0 UPnP/1.0 DLNADOC/1.50")

	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		code := xmlField(payload, "errorCode")
		description := xmlField(payload, "errorDescription")
		if code != "" {
			return nil, fmt.Errorf("%s failed: %s (code %s)", action, description, code)
		}
		return nil, fmt.Errorf("%s returned %s", action, response.Status)
	}
	return payload, nil
}

func xmlField(payload []byte, name string) string {
	var value struct {
		Value string `xml:",chardata"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != name {
			continue
		}
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return ""
		}
		return strings.TrimSpace(value.Value)
	}
}

func formatSeekTime(position time.Duration) string {
	total := int64(position / time.Second)
	if total < 0 {
		total = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, total%3600/60, total%60)
}

func appendEscaped(builder *strings.Builder, text string) {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(text))
	builder.Write(buffer.Bytes())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
