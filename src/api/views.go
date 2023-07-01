package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"nwi.io/nwi/models"
	"nwi.io/nwi/serializers"
)

type authError struct {
	Message string
}

func (e *authError) Error() string {
	return e.Message
}

func getGeoid(address string) (string, error) {
	var geoidResults serializers.GeoCodingResult
	url := "https://geocoding.geo.census.gov/geocoder/geographies/onelineaddress?address=" + address + "&benchmark=2020&vintage=Census2010_Census2020&format=json"
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Golang_Spider_Bot/3.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &geoidResults); err != nil { // Parse []byte to go struct pointer
		return "", err
	}
	if len(geoidResults.Result.AddressMatches) > 0 {
		return geoidResults.Result.AddressMatches[0].Geographies.CensusBlocks[0].Geoid[:len(geoidResults.Result.AddressMatches[0].Geographies.CensusBlocks[0].Geoid)-3], nil
	} else {
		return "", nil
	}
}

func (h handler) GetScores(ctx *gin.Context) {
	address := strings.ReplaceAll(ctx.Query("address"), " ", "%20")
	if address != "" {
		var wg sync.WaitGroup
		wg.Add(1)
		geoid := make(chan string)
		start := time.Now()
		go func() {
			wg.Done()
			res, err := getGeoid(address)
			if err != nil {
				ctx.AbortWithError(http.StatusNotFound, err)
			}
			geoid <- res
		}()
		var score models.Rank
		var cbsa models.CBSA
		geoid10, err := strconv.ParseUint(<-geoid, 10, 64)
		duration := time.Since(start)
		log.Printf("Geoid: %v took %v to execute. \n", geoid10, duration)
		if err != nil {
			ctx.AbortWithError(http.StatusNotFound, err)
			return
		}
		if result := h.DB.Where("geoid=?", geoid10).First(&score); result.Error != nil {
			ctx.AbortWithError(http.StatusNotFound, result.Error)
			return
		}
		if result := h.DB.Where(&models.CBSA{Geoid: geoid10}).First(&cbsa); result.Error != nil {
			ctx.AbortWithError(http.StatusNotFound, result.Error)
			return
		}
		switch format := ctx.Query("format"); format {
		case "json":
			result := serializers.AddressScoreResult{
				Geoid:                          geoid10,
				NWI:                            score.NWI,
				SearchedAddress:                ctx.Query("address"),
				RegionalTransitUsagePercentage: cbsa.PublicTansitPercentage,
				RegionalTransitUsage:           cbsa.PublicTansitEstimate,
				RegionalBikeRidership:          cbsa.BikeRidership,
				Format:                         format,
			}
			ctx.JSON(http.StatusOK, &result)
		case "xml":
			result := serializers.AddressScoreResultXML{
				Geoid:                          geoid10,
				NWI:                            score.NWI,
				SearchedAddress:                ctx.Query("address"),
				RegionalTransitUsagePercentage: cbsa.PublicTansitPercentage,
				RegionalTransitUsage:           cbsa.PublicTansitEstimate,
				RegionalBikeRidership:          cbsa.BikeRidership,
				Format:                         format,
			}
			ctx.JSON(http.StatusOK, &result)
		default:
			ctx.AbortWithError(http.StatusBadRequest, fmt.Errorf("views.go: unknown parameter: %s", format))
		}

		wg.Wait()
	} else {
		var scores []models.Rank
		var zipScores []models.Rank
		var res []models.ZipCode
		zipcode := ctx.Query("zipcode")
		limit, limit_err := strconv.Atoi(ctx.Query("limit"))
		if limit_err != nil {
			limit = 50
		}
		offset, offset_err := strconv.Atoi(ctx.Query("offset"))
		if offset_err != nil {
			offset = 0
		}
		query := serializers.ScoresQuery{
			Limit:  limit,
			Offset: offset,
		}
		if zipcode == "" {
			if result := h.DB.Limit(query.Limit).Offset(query.Offset).Find(&scores); result.Error != nil {
				ctx.AbortWithError(http.StatusNotFound, result.Error)
				return
			}
		} else {
			if result := h.DB.Limit(query.Limit).Offset(query.Offset).Where("zipcode=?", zipcode).Find(&res); result.Error != nil {
				ctx.AbortWithError(http.StatusNotFound, result.Error)
				return
			}
			for _, item := range res {
				if result := h.DB.Limit(query.Limit).Offset(query.Offset).Where("cbsa=?", item.CBSA).Model(&models.Rank{}).Select("*").Joins("left join cbsas on cbsas.geoid = ranks.geoid").Scan(&zipScores); result.Error != nil {
					fmt.Println(result.Error)
				}
				scores = append(scores, zipScores...)
			}
		}
		switch format := ctx.Query("format"); format {
		case "json":
			results := []serializers.ScoreResult{}
			for i := range scores {
				var csa models.CSA
				var cbsa models.CBSA
				if csa_result := h.DB.Where(&models.CSA{Geoid: scores[i].Geoid}).First(&csa); csa_result.Error != nil {
					csa.CSA_name = ""
				}
				if cbsa_result := h.DB.Where(&models.CBSA{Geoid: scores[i].Geoid}).First(&cbsa); cbsa_result.Error != nil {
					cbsa.CBSA_name = ""
				}

				results = append(
					results,
					serializers.ScoreResult{
						ID:                             i + offset,
						Geoid:                          scores[i].Geoid,
						CSA_name:                       csa.CSA_name,
						CBSA_name:                      cbsa.CBSA_name,
						NWI:                            scores[i].NWI,
						RegionalTransitUsagePercentage: cbsa.PublicTansitPercentage,
						RegionalTransitUsage:           cbsa.PublicTansitEstimate,
						RegionalBikeRidership:          cbsa.BikeRidership,
						Format:                         format,
					},
				)
			}
			ctx.JSON(http.StatusOK, &results)
		case "xml":
			results := []serializers.ScoreResultXML{}
			for i := range scores {
				var csa models.CSA
				var cbsa models.CBSA
				if csa_result := h.DB.Where(&models.CSA{Geoid: scores[i].Geoid}).First(&csa); csa_result.Error != nil {
					csa.CSA_name = ""
				}
				if cbsa_result := h.DB.Where(&models.CBSA{Geoid: scores[i].Geoid}).First(&cbsa); cbsa_result.Error != nil {
					cbsa.CBSA_name = ""
				}

				results = append(
					results,
					serializers.ScoreResultXML{
						ID:                             i + offset,
						Geoid:                          scores[i].Geoid,
						CSA_name:                       csa.CSA_name,
						CBSA_name:                      cbsa.CBSA_name,
						NWI:                            scores[i].NWI,
						RegionalTransitUsagePercentage: cbsa.PublicTansitPercentage,
						RegionalTransitUsage:           cbsa.PublicTansitEstimate,
						RegionalBikeRidership:          cbsa.BikeRidership,
						Format:                         format,
					},
				)
			}
			ctx.JSON(http.StatusOK, &results)
		}

	}
}
