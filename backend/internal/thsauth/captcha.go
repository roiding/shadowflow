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
	captchaMaxPixels     = 4 << 20
	captchaScanInterval  = 64
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
	x, err := detectCaptchaOffsetContext(ctx, background, piece)
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
	return detectCaptchaOffsetContext(context.Background(), backgroundBytes, pieceBytes)
}

func detectCaptchaOffsetContext(ctx context.Context, backgroundBytes, pieceBytes []byte) (float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	background, err := decodeCaptchaImage(ctx, backgroundBytes)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return 0, contextError
		}
		return 0, fmt.Errorf("%w: invalid captcha background", ErrChallenge)
	}
	piece, err := decodeCaptchaImage(ctx, pieceBytes)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return 0, contextError
		}
		return 0, fmt.Errorf("%w: invalid captcha piece", ErrChallenge)
	}
	backgroundBounds := background.Bounds()
	pieceBounds := piece.Bounds()
	if backgroundBounds.Dx() < 100 || backgroundBounds.Dy() < 80 ||
		pieceBounds.Dx() < 20 || pieceBounds.Dy() < 20 ||
		pieceBounds.Dx() > backgroundBounds.Dx() || pieceBounds.Dy() > backgroundBounds.Dy() {
		return 0, fmt.Errorf("%w: unsupported captcha image dimensions", ErrChallenge)
	}
	template, err := prepareCaptchaTemplate(ctx, piece)
	if err != nil {
		return 0, err
	}
	backgroundGray, err := grayscaleImage(ctx, background)
	if err != nil {
		return 0, err
	}
	backgroundWidth := backgroundBounds.Dx()
	pieceWidth, pieceHeight := pieceBounds.Dx(), pieceBounds.Dy()
	bestScore := -1.0
	bestX := 0
	scanned := 0
	for row := 0; row+pieceHeight <= backgroundBounds.Dy(); row++ {
		for column := 0; column+pieceWidth <= backgroundBounds.Dx(); column++ {
			if scanned%captchaScanInterval == 0 {
				if err := ctx.Err(); err != nil {
					return 0, err
				}
			}
			scanned++
			score := captchaSimilarity(backgroundGray, backgroundWidth, template, column, row)
			if score > bestScore {
				bestScore = score
				bestX = column
			}
		}
	}
	if bestScore < 0.5 {
		return 0, fmt.Errorf("%w: captcha piece was not located", ErrChallenge)
	}
	return float64(bestX), nil
}

type captchaTemplate struct {
	positions []image.Point
	values    []float64
	mean      float64
	energy    float64
}

func decodeCaptchaImage(ctx context.Context, payload []byte) (image.Image, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > captchaMaxPixels/config.Height {
		return nil, fmt.Errorf("%w: captcha image exceeds pixel limit", ErrChallenge)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func prepareCaptchaTemplate(ctx context.Context, piece image.Image) (captchaTemplate, error) {
	bounds := piece.Bounds()
	positions := make([]image.Point, 0, bounds.Dx()*bounds.Dy())
	values := make([]float64, 0, bounds.Dx()*bounds.Dy())
	for pieceRow := 0; pieceRow < bounds.Dy(); pieceRow++ {
		if pieceRow%captchaScanInterval == 0 {
			if err := ctx.Err(); err != nil {
				return captchaTemplate{}, err
			}
		}
		for pieceColumn := 0; pieceColumn < bounds.Dx(); pieceColumn++ {
			red, green, blue, alpha := piece.At(bounds.Min.X+pieceColumn, bounds.Min.Y+pieceRow).RGBA()
			if alpha>>8 < 200 {
				continue
			}
			positions = append(positions, image.Pt(pieceColumn, pieceRow))
			values = append(values, captchaGray(red, green, blue))
		}
	}
	if len(values) < 200 {
		return captchaTemplate{}, fmt.Errorf("%w: captcha piece was not located", ErrChallenge)
	}
	if err := ctx.Err(); err != nil {
		return captchaTemplate{}, err
	}
	mean := mean(values)
	energy := 0.0
	for _, value := range values {
		centered := value - mean
		energy += centered * centered
	}
	return captchaTemplate{positions: positions, values: values, mean: mean, energy: energy}, nil
}

func grayscaleImage(ctx context.Context, source image.Image) ([]float64, error) {
	bounds := source.Bounds()
	gray := make([]float64, bounds.Dx()*bounds.Dy())
	for row := 0; row < bounds.Dy(); row++ {
		if row%captchaScanInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for column := 0; column < bounds.Dx(); column++ {
			red, green, blue, _ := source.At(bounds.Min.X+column, bounds.Min.Y+row).RGBA()
			gray[row*bounds.Dx()+column] = captchaGray(red, green, blue)
		}
	}
	return gray, nil
}

func captchaSimilarity(background []float64, stride int, template captchaTemplate, left, top int) float64 {
	total := 0.0
	squares := 0.0
	products := 0.0
	for index, position := range template.positions {
		value := background[(top+position.Y)*stride+left+position.X]
		total += value
		squares += value * value
		products += value * template.values[index]
	}
	count := float64(len(template.values))
	backgroundMean := total / count
	backgroundEnergy := squares - count*backgroundMean*backgroundMean
	numerator := products - count*backgroundMean*template.mean
	denominator := math.Sqrt(backgroundEnergy * template.energy)
	if denominator <= 0 || math.IsNaN(denominator) {
		return -1
	}
	return numerator / denominator
}

func captchaGray(red, green, blue uint32) float64 {
	return 0.299*float64(red>>8) + 0.587*float64(green>>8) + 0.114*float64(blue>>8)
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
