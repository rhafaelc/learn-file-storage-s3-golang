package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

type VideoMetadata struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "COuldn't validate JWT", err)
		return
	}

	fmt.Println("uploading video for video", videoID, "by user", userID)

	videoDb, err := cfg.db.GetVideo(videoID)
	if err != nil {
		if err == sql.ErrNoRows {
			respondWithError(w, http.StatusNotFound, "Video not found", err)
			return
		}
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't parse media type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Only accept video/mp4", nil)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temporrary file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy data", err)
		return
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't reset file pointer", err)
		return
	}

	processedVideoPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't process video for fast start",
			err,
		)
		return
	}
	processedVideoFile, err := os.Open(processedVideoPath)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't open processed video file",
			err,
		)
		return
	}
	defer processedVideoFile.Close()

	aspectRatio, err := getVideoAspectRatio(processedVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video aspect ratio", err)
		return
	}
	if aspectRatio == "16:9" {
		aspectRatio = "landscape"
	} else if aspectRatio == "9:16" {
		aspectRatio = "portrait"
	}

	key := fmt.Sprintf("%s/%s", aspectRatio, getAssetPath(mediaType))
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(key),
		Body:        processedVideoFile,
		ContentType: aws.String(mediaType),
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload video to S3", err)
		return
	}

	videoURL := fmt.Sprintf("%s,%s", cfg.s3Bucket, key)
	videoDb.VideoURL = &videoURL

	if err := cfg.db.UpdateVideo(videoDb); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	presignedVideo, err := cfg.dbVideoToSignedVideo(videoDb)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't generate presigned video url",
			err,
		)
		return
	}
	respondWithJSON(w, http.StatusOK, presignedVideo)
}

func generatePresignedURL(
	s3Client *s3.Client,
	bucket, key string,
	expireTime time.Duration,
) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)
	obj, err := presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned url: %v", err)
	}

	return obj.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	parts := strings.Split(*video.VideoURL, ",")
	if len(parts) != 2 {
		return video, nil
	}
	bucket := parts[0]
	key := parts[1]

	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 5*time.Minute)
	if err != nil {
		return database.Video{}, err
	}

	video.VideoURL = &presignedURL
	return video, nil
}

func processVideoForFastStart(filepath string) (string, error) {
	outputFilePath := filepath + ".processing"
	cmd := exec.Command("ffmpeg",
		"-i",
		filepath,
		"-c",
		"copy",
		"-movflags",
		"faststart",
		"-f",
		"mp4",
		outputFilePath,
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return outputFilePath, nil
}

func getVideoAspectRatio(filepath string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v",
		"error",
		"-print_format",
		"json",
		"-show_streams",
		filepath,
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	metadata := &VideoMetadata{}
	if err := json.Unmarshal(out.Bytes(), metadata); err != nil {
		return "", err
	}

	width := metadata.Streams[0].Width
	height := metadata.Streams[0].Height
	tolerance := 0.01

	aspectRatio := checkAspectRatioType(width, height, tolerance)

	return aspectRatio, nil
}

func checkAspectRatioType(width, height int, tolerance float64) string {
	sixteenNineRatio := 16.0 / 9.0
	nineSixteenRatio := 9.0 / 16.0

	ratio := float64(width) / float64(height)

	if math.Abs(ratio-sixteenNineRatio) <= tolerance {
		return "16:9"
	}
	if math.Abs(ratio-nineSixteenRatio) <= tolerance {
		return "9:16"
	}
	return "other"
}
