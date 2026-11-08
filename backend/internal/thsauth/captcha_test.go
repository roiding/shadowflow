package thsauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

func captchaFixture(testingT *testing.T, backgroundName, pieceName string) ([]byte, []byte) {
	testingT.Helper()
	background, err := os.ReadFile(backgroundName)
	if err != nil {
		testingT.Fatal(err)
	}
	piece, err := os.ReadFile(pieceName)
	if err != nil {
		testingT.Fatal(err)
	}
	return background, piece
}

func TestDetectCaptchaOffsetUsesOfficialImage(t *testing.T) {
	background, piece := captchaFixture(t,
		"testdata/captcha-background.jpg",
		"testdata/captcha-piece.png")
	offset, err := detectCaptchaOffset(background, piece)
	if err != nil {
		t.Fatal(err)
	}
	if offset < 150 || offset > 160 {
		t.Fatalf("unexpected captcha offset: %.1f", offset)
	}
}

func TestDetectCaptchaOffsetSyntheticShape(t *testing.T) {
	background := image.NewRGBA(image.Rect(0, 0, 340, 195))
	for y := 0; y < 195; y++ {
		for x := 0; x < 340; x++ {
			background.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	piece := image.NewRGBA(image.Rect(0, 0, 55, 42))
	for y := 0; y < 42; y++ {
		for x := 0; x < 55; x++ {
			piece.SetRGBA(x, y, background.RGBAAt(x+83, y+61))
		}
	}
	var backgroundBuffer, pieceBuffer bytes.Buffer
	if err := jpeg.Encode(&backgroundBuffer, background, nil); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&pieceBuffer, piece); err != nil {
		t.Fatal(err)
	}
	offset, err := detectCaptchaOffset(backgroundBuffer.Bytes(), pieceBuffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if offset < 80 || offset > 86 {
		t.Fatalf("unexpected synthetic captcha offset: %.1f", offset)
	}
}

func TestDetectCaptchaOffsetRejectsBrokenImages(t *testing.T) {
	if _, err := detectCaptchaOffset([]byte("not-an-image"), []byte("also-not")); err == nil {
		t.Fatal("broken captcha images were accepted")
	}
}

func TestCaptchaSolutionPhraseUsesServerInitYAndDisplayScale(t *testing.T) {
	challenge := captchaPreHandle{}
	challenge.Code = replyCodePtr(0)
	challenge.Data.Sign = "fixture-signature"
	challenge.Data.URLParams = "rand=fixture&time=1&appid=registernew&captcha_type=4&signature=fixture-signature"
	challenge.Data.Images = []string{"background-key", "piece-key"}
	challenge.Data.InitY = 114
	background, piece := captchaFixture(t,
		"testdata/captcha-background.jpg",
		"testdata/captcha-piece.png")
	var getTicketQuery string
	client := &http.Client{Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		switch request.URL.Path {
		case "/getPreHandle":
			payload := `PreHandle({"data":{"src1":"getImg","src2":"getTicket","sign":"fixture-signature","urlParams":"rand=fixture&time=1&appid=registernew&captcha_type=4&signature=fixture-signature","problem":"","imgs":["background-key","piece-key"],"initx":60,"inity":114},"code":0,"msg":"ok"})`
			response.Body = io.NopCloser(strings.NewReader(payload))
		case "/getImg":
			payload := background
			if request.URL.Query().Get("iuk") == "piece-key" {
				payload = piece
			}
			response.Body = io.NopCloser(bytes.NewReader(payload))
		case "/getTicket":
			getTicketQuery = request.URL.RawQuery
			response.Body = io.NopCloser(strings.NewReader(`verify({"code":0,"msg":"ok","ticket":"fixture-ticket"})`))
		default:
			t.Fatalf("unexpected captcha path %s", request.URL.Path)
		}
		return response, nil
	})}
	solution, err := solveCaptcha(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if solution.Signature != "fixture-signature" || solution.Ticket != "fixture-ticket" {
		t.Fatalf("unexpected captcha solution: %+v", solution)
	}
	if !strings.Contains(getTicketQuery, "phrase=") || !strings.Contains(getTicketQuery, "%3B") {
		t.Fatalf("captcha phrase was not submitted: %s", getTicketQuery)
	}
	if !strings.Contains(getTicketQuery, "103.605882") {
		t.Fatalf("captcha phrase did not use the display scale: %s", getTicketQuery)
	}
	phraseValue := strings.SplitN(strings.SplitN(getTicketQuery, "phrase=", 2)[1], "&", 2)[0]
	phraseFields := strings.Split(phraseValue, "%3B")
	if len(phraseFields) != 4 || !regexp.MustCompile(`^\d+$`).MatchString(phraseFields[0]) {
		t.Fatalf("captcha phrase x must be an integer display position: %s", phraseValue)
	}
}

func TestPasswordLoginSolvesCaptchaAndRetriesLogin(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyDocument := fmt.Sprintf(`var thspubkey={"v":"fixture-key","m":"%X","p":"%X"};`, privateKey.N, privateKey.E)
	background, piece := captchaFixture(t,
		"testdata/captcha-background.jpg",
		"testdata/captcha-piece.png")
	jar, _ := cookiejar.New(nil)
	submissions := 0
	captchaCalls := 0
	nonce := ""
	client := &http.Client{Jar: jar, Transport: passwordTransport(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		payload := ""
		switch request.URL.Path {
		case "/login":
			payload = "<html><title>official login fixture</title></html>"
		case "/pubkey/default.js":
			payload = keyDocument
		case "/user/getGS":
			body, _ := io.ReadAll(request.Body)
			values, _ := url.ParseQuery(string(body))
			nonce = values.Get("crnd")
			salt := saltResponse{DSK: "fixture-dsk", DSV: "fixture-dsv"}
			salt.SSV = base64.StdEncoding.EncodeToString(xorBytes([]byte("gs=fixture-salt"), []byte(sha256Hex(nonce+salt.DSK))))
			payload = fmt.Sprintf(`{"errorcode":0,"dsk":%q,"dsv":%q,"ssv":%q}`, salt.DSK, salt.DSV, salt.SSV)
		case "/login/dologinreturnjson2":
			submissions++
			body, _ := io.ReadAll(request.Body)
			values, _ := url.ParseQuery(string(body))
			if submissions == 1 {
				if values.Get("captcha_ticket") != "" || values.Get("captcha_phrase") != "" {
					t.Fatal("unexpected captcha fields before challenge")
				}
				payload = `{"errorcode":-11400,"errormsg":"slider required"}`
			} else {
				if values.Get("captcha_ticket") != "fixture-ticket" || values.Get("captcha_signature") != "fixture-signature" {
					t.Fatal("captcha fields were not submitted")
				}
				payload = `{"errorcode":0}`
				response.Header.Set("Set-Cookie", "ticket=fixture-session; Domain=.10jqka.com.cn; Path=/; Secure; HttpOnly")
			}
		case "/mobileapi/hotspot_focus/chart_config/v1/query_config":
			payload = `{"status_code":0,"data":{}}`
		case "/getPreHandle":
			captchaCalls++
			payload = `PreHandle({"data":{"src1":"getImg","src2":"getTicket","sign":"fixture-signature","urlParams":"rand=fixture&captcha_type=4&signature=fixture-signature","imgs":["background","piece"],"initx":60,"inity":114},"code":0,"msg":"ok"})`
		case "/getImg":
			payloadBytes := background
			if request.URL.Query().Get("iuk") == "piece" {
				payloadBytes = piece
			}
			response.Body = io.NopCloser(bytes.NewReader(payloadBytes))
			return response, nil
		case "/getTicket":
			payload = `verify({"code":0,"msg":"ok","ticket":"fixture-ticket"})`
		default:
			t.Fatalf("unexpected login path %s", request.URL.Path)
		}
		response.Body = io.NopCloser(strings.NewReader(payload))
		return response, nil
	})}
	if err := PasswordLogin(passwordFixtureCredentials(t))(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if submissions != 2 || captchaCalls != 1 {
		t.Fatalf("unexpected retry counts: submissions=%d captcha=%d", submissions, captchaCalls)
	}
}

func TestCaptchaJSONPRejectsHTML(t *testing.T) {
	var challenge captchaPreHandle
	if err := decodeCaptchaJSONP([]byte("<html>blocked</html>"), &challenge); err == nil {
		t.Fatal("HTML was accepted as captcha JSONP")
	}
	if err := decodeCaptchaJSONP([]byte(`PreHandle({"code":0})`), &challenge); err != nil {
		t.Fatal(err)
	}
}

func TestCaptchaPhraseUsesInitialY(t *testing.T) {
	challenge := captchaPreHandle{}
	challenge.Data.InitY = 114
	displayHeight := captchaDisplayWidth / captchaImageWidth * captchaImageHeight
	displayY := challenge.Data.InitY / captchaImageHeight * displayHeight
	if displayY != 103.60588235294118 {
		t.Fatalf("unexpected display y: %v", displayY)
	}
}

func TestCaptchaFixtureEncoding(t *testing.T) {
	background, piece := captchaFixture(t,
		"testdata/captcha-background.jpg",
		"testdata/captcha-piece.png")
	defer func() {
		background = nil
		piece = nil
	}()
	if len(background) == 0 || len(piece) == 0 {
		t.Fatal("captcha fixture is empty")
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(background)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(piece)); err != nil {
		t.Fatal(err)
	}
	_ = base64.StdEncoding
}
