package cast

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseDescriptionBilibiliTV(t *testing.T) {
	location := "http://192.168.1.20:9958/bilibili/description.xml"
	document := `<?xml version="1.0" encoding="UTF-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
    <friendlyName>我的小电视</friendlyName>
    <manufacturer>Bilibili Inc.</manufacturer>
    <modelDescription>云视听小电视</modelDescription>
    <modelName>Smart TV Pro</modelName>
    <UDN>uuid:XXC006DCEB6F8A3440358B4BC1B01CD4FB663</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
        <controlURL>/bilibili/AVTransport/XXC/control.xml</controlURL>
      </service>
      <service>
        <serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType>
        <controlURL>/bilibili/RenderingControl/XXC/control.xml</controlURL>
      </service>
    </serviceList>
  </device>
</root>`
	device, err := parseDescription(location, []byte(document))
	if err != nil {
		t.Fatalf("parseDescription: %v", err)
	}
	if device.ID != "uuid:XXC006DCEB6F8A3440358B4BC1B01CD4FB663" {
		t.Errorf("ID = %q", device.ID)
	}
	if device.Name != "我的小电视" {
		t.Errorf("Name = %q", device.Name)
	}
	if device.Model != "云视听小电视" {
		t.Errorf("Model = %q", device.Model)
	}
	if device.Host != "192.168.1.20" {
		t.Errorf("Host = %q", device.Host)
	}
	wantControl := "http://192.168.1.20:9958/bilibili/AVTransport/XXC/control.xml"
	if device.AVTransportURL != wantControl {
		t.Errorf("AVTransportURL = %q, want %q", device.AVTransportURL, wantControl)
	}
	if !strings.HasSuffix(device.RenderingControlURL, "/bilibili/RenderingControl/XXC/control.xml") {
		t.Errorf("RenderingControlURL = %q", device.RenderingControlURL)
	}
}

func TestParseDescriptionUsesURLBase(t *testing.T) {
	location := "http://192.168.1.30:16938/description.xml"
	document := `<root><URLBase>http://192.168.1.30:16938</URLBase><device>
		<deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>
		<friendlyName>Windows Media Player</friendlyName>
		<UDN>uuid:renderer</UDN>
		<serviceList><service>
			<serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
			<controlURL>AVTransport/uuid/control</controlURL>
		</service></serviceList>
	</device></root>`
	device, err := parseDescription(location, []byte(document))
	if err != nil {
		t.Fatalf("parseDescription: %v", err)
	}
	want := "http://192.168.1.30:16938/AVTransport/uuid/control"
	if device.AVTransportURL != want {
		t.Errorf("AVTransportURL = %q, want %q", device.AVTransportURL, want)
	}
}

func TestParseDescriptionRejectsNonRenderer(t *testing.T) {
	document := `<root><device>
		<deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
		<friendlyName>NAS</friendlyName>
	</device></root>`
	if _, err := parseDescription("http://192.168.1.40/desc.xml", []byte(document)); err == nil {
		t.Fatal("expected an error for non-renderer devices")
	}
}

func TestSSDPLocation(t *testing.T) {
	message := "HTTP/1.1 200 OK\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\nLOCATION: http://192.168.1.20:9958/bilibili/description.xml\r\n\r\n"
	if got := ssdpLocation([]byte(message)); got != "http://192.168.1.20:9958/bilibili/description.xml" {
		t.Errorf("ssdpLocation = %q", got)
	}
	if got := ssdpLocation([]byte("HTTP/1.1 200 OK\r\n\r\n")); got != "" {
		t.Errorf("ssdpLocation without header = %q", got)
	}
}

func TestVideoMetadataEscapes(t *testing.T) {
	metadata := VideoMetadata(`标题 & <测试>`, "http://192.168.1.5:8080/s/a/master.m3u8?x=1&y=2", "application/vnd.apple.mpegurl")
	for _, want := range []string{
		"<dc:title>标题 &amp; &lt;测试&gt;</dc:title>",
		`protocolInfo="http-get:*:application/vnd.apple.mpegurl:*"`,
		"http://192.168.1.5:8080/s/a/master.m3u8?x=1&amp;y=2",
	} {
		if !strings.Contains(metadata, want) {
			t.Errorf("metadata missing %q:\n%s", want, metadata)
		}
	}
}

func TestFormatSeekTime(t *testing.T) {
	cases := []struct {
		input time.Duration
		want  string
	}{
		{0, "00:00:00"},
		{83 * time.Second, "00:01:23"},
		{time.Hour + 2*time.Minute + 5*time.Second, "01:02:05"},
		{-time.Second, "00:00:00"},
	}
	for _, test := range cases {
		if got := formatSeekTime(test.input); got != test.want {
			t.Errorf("formatSeekTime(%v) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestSoapActionSendsEscapedArguments(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("SOAPAction"); got != `"`+avTransportType+`#SetAVTransportURI"` {
			t.Errorf("SOAPAction = %q", got)
		}
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		_, _ = io.WriteString(w, `<s:Envelope><s:Body><u:SetAVTransportURIResponse xmlns:u="`+avTransportType+`"/></s:Body></s:Envelope>`)
	}))
	defer server.Close()

	metadata := VideoMetadata("标题", "http://192.168.1.5:9000/s/id/master.m3u8", "application/vnd.apple.mpegurl")
	if err := SetAVTransportURI(context.Background(), server.URL, "http://192.168.1.5:9000/s/id/master.m3u8", metadata); err != nil {
		t.Fatalf("SetAVTransportURI: %v", err)
	}
	for _, want := range []string{
		"<InstanceID>0</InstanceID>",
		"<CurrentURI>http://192.168.1.5:9000/s/id/master.m3u8</CurrentURI>",
		"&lt;DIDL-Lite",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SOAP body missing %q:\n%s", want, body)
		}
	}
}

func TestTransportInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), "GetTransportInfo") {
			t.Errorf("unexpected action body: %s", data)
		}
		_, _ = io.WriteString(w, `<s:Envelope><s:Body><u:GetTransportInfoResponse xmlns:u="`+avTransportType+`">
		  <CurrentTransportState>PLAYING</CurrentTransportState>
		  <CurrentTransportStatus>OK</CurrentTransportStatus>
		  <CurrentSpeed>1</CurrentSpeed>
		</u:GetTransportInfoResponse></s:Body></s:Envelope>`)
	}))
	defer server.Close()

	state, status, err := TransportInfo(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("TransportInfo: %v", err)
	}
	if state != "PLAYING" || status != "OK" {
		t.Errorf("TransportInfo = %q/%q", state, status)
	}
}

func TestSoapActionSurfacesUPnPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `<s:Envelope><s:Body><s:Fault><detail><UPnPError>
		  <errorCode>701</errorCode><errorDescription>Transition not available</errorDescription>
		</UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
	}))
	defer server.Close()

	err := Play(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "701") || !strings.Contains(err.Error(), "Transition not available") {
		t.Fatalf("Play error = %v", err)
	}
}
