package thsauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

const (
	captchaPreHandleURL = "https://captcha.10jqka.com.cn/getPreHandle"
	captchaImageURL     = "https://captcha.10jqka.com.cn/getImg"
	captchaTicketURL    = "https://captcha.10jqka.com.cn/getTicket"

	captchaDisplayWidth  = 309.0
	captchaImageWidth    = 340.0
	captchaImageHeight   = 195.0
	captchaMaxImages     = 2 << 20
	captchaMaxJSONPBytes = 256 << 10
)

var captchaJSONP = regexp.MustCompile(`(?s)^\s*[A-Za-z_$][A-Za-z0-9_$]*\s*\((.*)\)\s*;?\s*$`)

type captchaPreHandle struct {
	Code *int   `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Sign      string   `json:"sign"`
		URLParams string   `json:"urlParams"`
		Images    []string `json:"imgs"`
		InitX     float64  `json:"initx"`
		InitY     float64  `json:"inity"`
	} `json:"data"`
}

type captchaTicket struct {
	Code   *int   `json:"code"`
	Msg    string `json:"msg"`
	Ticket string `json:"ticket"`
}

type captchaSolution struct {
	Phrase    string
	Ticket    string
	Signature string
}

func solveCaptcha(ctx context.Context, client *http.Client) (captchaSolution, error) {
	challenge, err := fetchCaptchaChallenge(ctx, client)
	if err != nil {
		return captchaSolution{}, err
	}
	background, err := fetchCaptchaImage(ctx, client, challenge.Data.URLParams, challenge.Data.Images[0])
	if err != nil {
		return captchaSolution{}, err
	}
	piece, err := fetchCaptchaImage(ctx, client, challenge.Data.URLParams, challenge.Data.Images[1])
	if err != nil {
		return captchaSolution{}, err
	}
	x, err := detectCaptchaOffset(background, piece)
	if err != nil {
		return captchaSolution{}, err
	}
	// Official frontends submit the gap's absolute display position as an integer
	// first field (verified against a live accepted submission: "219;97.24...;309;177.22...").
	displayX := math.Round(x * captchaDisplayWidth / captchaImageWidth)
	displayHeight := captchaDisplayWidth / captchaImageWidth * captchaImageHeight
	displayY := challenge.Data.InitY / captchaImageHeight * displayHeight
	phrase := fmt.Sprintf("%.0f;%.15g;%.0f;%.15g", displayX, displayY, captchaDisplayWidth, displayHeight)
	ticket, err := fetchCaptchaTicket(ctx, client, challenge.Data.URLParams, phrase)
	if err != nil {
		return captchaSolution{}, err
	}
	return captchaSolution{
		Phrase:    phrase,
		Ticket:    ticket,
		Signature: challenge.Data.Sign,
	}, nil
}

func fetchCaptchaChallenge(ctx context.Context, client *http.Client) (captchaPreHandle, error) {
	query := url.Values{
		"captcha_type": {"4"},
		"appid":        {"registernew"},
		"random":       {strconv.FormatFloat(float64(time.Now().UnixNano())/1e6, 'f', 4, 64)},
		"callback":     {"PreHandle"},
	}
	body, err := captchaRequest(ctx, client, captchaPreHandleURL+"?"+query.Encode(), http.StatusOK)
	if err != nil {
		return captchaPreHandle{}, err
	}
	var challenge captchaPreHandle
	if err := decodeCaptchaJSONP(body, &challenge); err != nil {
		return captchaPreHandle{}, err
	}
	if challenge.Code == nil || *challenge.Code != 0 || challenge.Data.Sign == "" ||
		challenge.Data.URLParams == "" || len(challenge.Data.Images) != 2 ||
		challenge.Data.Images[0] == "" || challenge.Data.Images[1] == "" ||
		challenge.Data.InitY < 0 || challenge.Data.InitY > captchaImageHeight {
		return captchaPreHandle{}, fmt.Errorf("%w: invalid captcha challenge", ErrChallenge)
	}
	return challenge, nil
}

func fetchCaptchaImage(ctx context.Context, client *http.Client, params, imageKey string) ([]byte, error) {
	target := captchaImageURL + "?" + params + "&iuk=" + url.QueryEscape(imageKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid captcha image request", ErrChallenge)
	}
	request.Header.Set("User-Agent", captchaUserAgent)
	request.Header.Set("Referer", LoginURL+"/")
	request.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/*,*/*;q=0.8")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: captcha image unavailable", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: captcha image status %d", ErrChallenge, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, captchaMaxImages+1))
	if err != nil || len(payload) == 0 || len(payload) > captchaMaxImages {
		return nil, fmt.Errorf("%w: incomplete captcha image", ErrUnavailable)
	}
	return payload, nil
}

func fetchCaptchaTicket(ctx context.Context, client *http.Client, params, phrase string) (string, error) {
	query := url.Values{"phrase": {phrase}, "callback": {"verify"}}
	body, err := captchaRequest(ctx, client, captchaTicketURL+"?"+params+"&"+query.Encode(), http.StatusOK)
	if err != nil {
		return "", err
	}
	var ticket captchaTicket
	if err := decodeCaptchaJSONP(body, &ticket); err != nil {
		return "", err
	}
	if ticket.Code == nil || *ticket.Code != 0 || ticket.Ticket == "" {
		return "", fmt.Errorf("%w: captcha ticket rejected", ErrChallenge)
	}
	return ticket.Ticket, nil
}

func captchaRequest(ctx context.Context, client *http.Client, target string, status int) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid captcha request", ErrChallenge)
	}
	request.Header.Set("User-Agent", captchaUserAgent)
	request.Header.Set("Referer", LoginURL+"/")
	request.Header.Set("Accept", "*/*")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: captcha service unavailable", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		return nil, fmt.Errorf("%w: captcha service status %d", ErrChallenge, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, captchaMaxJSONPBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > captchaMaxJSONPBytes {
		return nil, fmt.Errorf("%w: incomplete captcha response", ErrUnavailable)
	}
	return payload, nil
}

func decodeCaptchaJSONP(body []byte, target any) error {
	match := captchaJSONP.FindSubmatch(bytes.TrimSpace(body))
	if len(match) != 2 {
		return fmt.Errorf("%w: invalid captcha JSONP", ErrChallenge)
	}
	if err := json.Unmarshal(match[1], target); err != nil {
		return fmt.Errorf("%w: invalid captcha payload", ErrChallenge)
	}
	return nil
}

func detectCaptchaOffset(backgroundBytes, pieceBytes []byte) (float64, error) {
	background, _, err := image.Decode(bytes.NewReader(backgroundBytes))
	if err != nil {
		return 0, fmt.Errorf("%w: invalid captcha background", ErrChallenge)
	}
	piece, _, err := image.Decode(bytes.NewReader(pieceBytes))
	if err != nil {
		return 0, fmt.Errorf("%w: invalid captcha piece", ErrChallenge)
	}
	backgroundBounds := background.Bounds()
	pieceBounds := piece.Bounds()
	if backgroundBounds.Dx() < 100 || backgroundBounds.Dy() < 80 ||
		pieceBounds.Dx() < 20 || pieceBounds.Dy() < 20 ||
		pieceBounds.Dx() > backgroundBounds.Dx() || pieceBounds.Dy() > backgroundBounds.Dy() {
		return 0, fmt.Errorf("%w: unsupported captcha image dimensions", ErrChallenge)
	}
	bestScore := -1.0
	bestX := 0
	for y := 0; y+pieceBounds.Dy() <= backgroundBounds.Dy(); y++ {
		for x := 0; x+pieceBounds.Dx() <= backgroundBounds.Dx(); x++ {
			score := captchaSimilarity(background, piece, x, y)
			if score > bestScore {
				bestScore = score
				bestX = x
			}
		}
	}
	if bestScore < 0.5 {
		return 0, fmt.Errorf("%w: captcha piece was not located", ErrChallenge)
	}
	return float64(bestX), nil
}

func captchaSimilarity(background, piece image.Image, left, top int) float64 {
	pieceBounds := piece.Bounds()
	width, height := pieceBounds.Dx(), pieceBounds.Dy()
	backgroundGray := make([]float64, 0, width*height)
	pieceGray := make([]float64, 0, width*height)
	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			red, green, blue, alpha := piece.At(pieceBounds.Min.X+px, pieceBounds.Min.Y+py).RGBA()
			if alpha>>8 < 200 {
				continue
			}
			backgroundRed, backgroundGreen, backgroundBlue, _ := background.At(left+px, top+py).RGBA()
			backgroundGray = append(backgroundGray, 0.299*float64(backgroundRed>>8)+0.587*float64(backgroundGreen>>8)+0.114*float64(backgroundBlue>>8))
			pieceGray = append(pieceGray, 0.299*float64(red>>8)+0.587*float64(green>>8)+0.114*float64(blue>>8))
		}
	}
	if len(pieceGray) < 200 {
		return -1
	}
	return pearsonCorrelation(backgroundGray, pieceGray)
}

func pearsonCorrelation(first, second []float64) float64 {
	firstMean, secondMean := mean(first), mean(second)
	numerator, firstEnergy, secondEnergy := 0.0, 0.0, 0.0
	for index := range first {
		firstValue := first[index] - firstMean
		secondValue := second[index] - secondMean
		numerator += firstValue * secondValue
		firstEnergy += firstValue * firstValue
		secondEnergy += secondValue * secondValue
	}
	denominator := math.Sqrt(firstEnergy * secondEnergy)
	if denominator == 0 {
		return -1
	}
	return numerator / denominator
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

const captchaUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"
