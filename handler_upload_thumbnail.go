package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()
	mediaType := header.Header.Get("Content-Type")

	data, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't read file", err)
		return
	}

	videoDb, err := cfg.db.GetVideo(videoID)
	if err != nil {
		if err == sql.ErrNoRows {
			respondWithError(w, http.StatusNotFound, "Video not found", nil)
			return
		}
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video", err)
		return
	}

	if videoDb.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	thumbnail := thumbnail{
		data:      data,
		mediaType: mediaType,
	}
	videoThumbnails[videoID] = thumbnail

	thumbnailURL := fmt.Sprintf("http://localhost:%v/api/thumbnails/%v", cfg.port, videoID)
	videoDb.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(videoDb)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoDb)
}
