package eastmoney

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/roiding/shadowflow/internal/graymarket"
)

func klineNumbers(fields []string, indexes ...int) ([12]float64, error) {
	var numbers [12]float64
	for _, index := range indexes {
		if index >= len(fields) || index >= len(numbers) {
			return numbers, fmt.Errorf("missing kline field %d", index)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(fields[index]), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return numbers, fmt.Errorf("invalid numeric kline field %d at %s", index, fields[0])
		}
		numbers[index] = value
	}
	return numbers, nil
}

func klineAmount(value float64, whole bool) (int64, error) {
	if value < 0 || value >= float64(math.MaxInt64) || math.IsNaN(value) || math.IsInf(value, 0) || whole && value != math.Trunc(value) {
		return 0, fmt.Errorf("invalid kline quantity %g", value)
	}
	return int64(value), nil
}

func validateKlinePrices(points []graymarket.StockKlinePoint, stock graymarket.RankRecord, allowCloseMismatch bool) error {
	if len(points) != 48 {
		return fmt.Errorf("expected 48 klines, got %d", len(points))
	}
	const tolerance = 0.0001
	for _, point := range points {
		for _, value := range []float64{point.OpenPrice, point.ClosePrice, point.HighPrice, point.LowPrice} {
			if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt64)/1e4 {
				return fmt.Errorf("invalid kline price at %s", point.SnapshotAt)
			}
		}
		if point.HighPrice+tolerance < max(point.OpenPrice, point.ClosePrice) || point.LowPrice-tolerance > min(point.OpenPrice, point.ClosePrice) || point.HighPrice < point.LowPrice {
			return fmt.Errorf("inconsistent kline OHLC at %s", point.SnapshotAt)
		}
		for _, value := range []float64{point.Amplitude, point.ChangePct, point.TurnoverRate} {
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) >= float64(math.MaxInt64)/1e6 {
				return fmt.Errorf("invalid kline ratio at %s", point.SnapshotAt)
			}
		}
		if math.IsNaN(point.ChangeValue) || math.IsInf(point.ChangeValue, 0) || math.Abs(point.ChangeValue) >= float64(math.MaxInt64)/1e4 || point.Amplitude < 0 || point.TurnoverRate < 0 {
			return fmt.Errorf("invalid kline change or ratio at %s", point.SnapshotAt)
		}
	}
	closeMatches := samePrice(points[47].ClosePrice, stock.ClosePrice) || allowCloseMismatch && math.Abs(points[47].ClosePrice-stock.ClosePrice) <= 0.0101
	if !samePrice(points[0].OpenPrice, stock.OpenPrice) || !samePrice(maxKlinePrice(points), stock.HighPrice) || !samePrice(minKlinePrice(points), stock.LowPrice) || !closeMatches {
		return fmt.Errorf("kline OHLC does not match daily bar")
	}
	return nil
}
