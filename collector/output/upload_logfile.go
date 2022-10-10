package output

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/aws/aws-sdk-go/service/s3/s3crypto"
	"github.com/pganalyze/collector/logs"
	"github.com/pganalyze/collector/state"
	"github.com/pganalyze/collector/util"
)

type keyHandler struct {
	plaintextKey  []byte
	ciphertextKey []byte
	cmkID         string

	CipherData s3crypto.CipherData
}

func generateBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func (kh *keyHandler) GenerateCipherData(keySize, ivSize int) (s3crypto.CipherData, error) {
	if keySize != len(kh.plaintextKey) {
		return s3crypto.CipherData{}, fmt.Errorf("Stored key size (%d) does not match requested (%d)", len(kh.plaintextKey), keySize)
	}

	matdesc := s3crypto.MaterialDescription{}
	matdesc["kms_cmk_id"] = &kh.cmkID

	cd := s3crypto.CipherData{
		Key:                 kh.plaintextKey,
		IV:                  generateBytes(ivSize),
		WrapAlgorithm:       s3crypto.KMSWrap,
		MaterialDescription: matdesc,
		EncryptedKey:        kh.ciphertextKey,
	}
	return cd, nil
}

func EncryptAndUploadLogfiles(httpClient *http.Client, s3 state.GrantS3, encryptionKey state.GrantLogsEncryptionKey, logger *util.Logger, logFiles []state.LogFile) []state.LogFile {
	if len(logFiles) == 0 {
		return logFiles
	}

	plaintextKey, err := base64.StdEncoding.DecodeString(encryptionKey.Plaintext)
	if err != nil {
		logger.PrintError("Could not decode log encryption key (plaintext)")
		return logFiles
	}
	ciphertextKey, err := base64.StdEncoding.DecodeString(encryptionKey.CiphertextBlob)
	if err != nil {
		logger.PrintError("Could not decode log encryption key (encrypted)")
		return logFiles
	}

	kh := keyHandler{plaintextKey: plaintextKey, ciphertextKey: ciphertextKey, cmkID: encryptionKey.KeyId}
	builder := s3crypto.AESGCMContentCipherBuilder(&kh)

	encryptor, err := builder.ContentCipher()
	if err != nil {
		logger.PrintError("Could not load content cipher: %s", err)
		return logFiles
	}

	for idx, logFile := range logFiles {
		content, _ := ioutil.ReadFile(logFile.TmpFile.Name())

		if len(logFile.FilterLogSecret) > 0 {
			content = logs.ReplaceSecrets(content, logFile.LogLines, logFile.FilterLogSecret)
		}

		dst := &bytesReadWriteSeeker{}
		md5 := newMD5Reader(bytes.NewReader(content))
		reader, err := encryptor.EncryptContents(md5)
		if err != nil {
			logger.PrintError("%s", err)
			return logFiles
		}

		_, err = io.Copy(dst, reader)
		if err != nil {
			logger.PrintError("%s", err)
			return logFiles
		}

		data := encryptor.GetCipherData()
		env, err := encodeMeta(md5, data)
		if err != nil {
			logger.PrintError("%s", err)
			return logFiles
		}

		dst.Seek(0, 0)
		encryptedContent, err := ioutil.ReadAll(dst)
		if err != nil {
			logger.PrintError("%s", err)
			return logFiles
		}

		formFields := make(map[string]string)
		for k, v := range s3.S3Fields {
			formFields[k] = v
		}

		formFields["x-amz-meta-x-amz-key-v2"] = env.CipherKey
		formFields["x-amz-meta-x-amz-iv"] = env.IV
		formFields["x-amz-meta-x-amz-matdesc"] = env.MatDesc
		formFields["x-amz-meta-x-amz-wrap-alg"] = env.WrapAlg
		formFields["x-amz-meta-x-amz-cek-alg"] = env.CEKAlg
		formFields["x-amz-meta-x-amz-tag-len"] = env.TagLen
		formFields["x-amz-meta-x-amz-unencrypted-content-md5"] = env.UnencryptedMD5
		formFields["x-amz-meta-x-amz-unencrypted-content-length"] = env.UnencryptedContentLen

		s3Location, err := uploadToS3(httpClient, s3.S3URL, formFields, logger, encryptedContent, logFile.UUID.String())
		if err != nil {
			logger.PrintError("Log S3 upload failed: %s", err)
			return logFiles
		}

		logFile.S3Location = s3Location
		logFile.S3CekAlgo = env.CEKAlg
		logFile.S3CmkKeyID = encryptionKey.KeyId
		logFile.ByteSize = int64(len(content))

		logFiles[idx] = logFile
	}

	return logFiles
}
