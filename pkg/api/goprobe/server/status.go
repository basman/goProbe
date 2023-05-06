package server

import (
	"net/http"

	gpapi "github.com/els0r/goProbe/pkg/api/goprobe"
	"github.com/gin-gonic/gin"
)

func (server *Server) getStatus(c *gin.Context) {
	iface := c.Param(ifaceKey)

	resp := &gpapi.StatusResponse{}
	resp.StatusCode = http.StatusOK
	resp.LastWriteout = server.writeoutHandler.LastRotation

	if iface != "" {
		resp.Statuses = server.captureManager.Status(iface)
	} else {
		// otherwise, fetch all
		resp.Statuses = server.captureManager.Status()
	}

	if len(resp.Statuses) == 0 {
		resp.StatusCode = http.StatusNoContent
	}

	c.JSON(resp.StatusCode, resp)
	return
}
