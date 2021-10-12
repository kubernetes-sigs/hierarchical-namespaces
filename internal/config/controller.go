package config

import (
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type NonLeaderController struct {
	controller.Controller
}

func (nlc *NonLeaderController) NeedLeaderElection() bool {
	return false
}

func AddNonLeaderCtrl(mgr manager.Manager, c controller.Controller) error {
	return mgr.Add(&NonLeaderController{c})
}
