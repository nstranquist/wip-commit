package main

import (
	"fmt"

	"github.com/nstranquist/wip-commit/internal/contract"
	skillbundle "github.com/nstranquist/wip-commit/skills/wip-commit"
)

type contractResult struct {
	contract.Document
	PortableBundleSHA256 string `json:"portable_bundle_sha256"`
}

func currentContractResult() (contractResult, error) {
	digest, err := skillbundle.PayloadDigest()
	if err != nil {
		return contractResult{}, err
	}
	return contractResult{Document: contract.Current(), PortableBundleSHA256: digest}, nil
}

func (result contractResult) human() string {
	return fmt.Sprintf("%s\nportable bundle %s", result.Protocol, result.PortableBundleSHA256)
}
