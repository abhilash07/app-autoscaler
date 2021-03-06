package scalingengine

import (
	"autoscaler/cf"
	"autoscaler/db"
	"autoscaler/models"

	"fmt"
	"strconv"
	"strings"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager"
)

type ScalingEngine interface {
	Scale(appId string, trigger *models.Trigger) (int, error)
	ComputeNewInstances(currentInstances int, adjustment string) (int, error)
	SetActiveSchedule(appId string, schedule *models.ActiveSchedule) error
	RemoveActiveSchedule(appId string, scheduleId string) error
}

type scalingEngine struct {
	logger          lager.Logger
	cfClient        cf.CfClient
	policyDB        db.PolicyDB
	scalingEngineDB db.ScalingEngineDB
	appLock         *StripedLock
	clock           clock.Clock
}

type ActiveScheduleNotFoundError struct {
}

func (ase *ActiveScheduleNotFoundError) Error() string {
	return fmt.Sprintf("active schedule not found")
}

func NewScalingEngine(logger lager.Logger, cfClient cf.CfClient, policyDB db.PolicyDB, scalingEngineDB db.ScalingEngineDB, clock clock.Clock) ScalingEngine {
	return &scalingEngine{
		logger:          logger.Session("scale"),
		cfClient:        cfClient,
		policyDB:        policyDB,
		scalingEngineDB: scalingEngineDB,
		appLock:         NewStripedLock(32),
		clock:           clock,
	}
}

func (s *scalingEngine) Scale(appId string, trigger *models.Trigger) (int, error) {
	logger := s.logger.WithData(lager.Data{"appId": appId})

	s.appLock.GetLock(appId).Lock()
	defer s.appLock.GetLock(appId).Unlock()

	now := s.clock.Now()
	history := &models.AppScalingHistory{
		AppId:        appId,
		Timestamp:    now.UnixNano(),
		ScalingType:  models.ScalingTypeDynamic,
		OldInstances: -1,
		NewInstances: -1,
		Reason:       getDynamicScalingReason(trigger),
	}

	defer s.scalingEngineDB.SaveScalingHistory(history)

	instances, err := s.cfClient.GetAppInstances(appId)
	if err != nil {
		logger.Error("failed-to-get-app-instances", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to get app instances"
		return -1, err
	}
	history.OldInstances = instances

	ok, err := s.scalingEngineDB.CanScaleApp(appId)
	if err != nil {
		logger.Error("failed-check-cooldown", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to check app cooldown setting"
		return -1, err
	}
	if !ok {
		history.Status = models.ScalingStatusIgnored
		history.NewInstances = instances
		history.Message = "app in cooldown period"
		return instances, nil
	}

	newInstances, err := s.ComputeNewInstances(instances, trigger.Adjustment)
	if err != nil {
		logger.Error("failed-compute-new-instance", err, lager.Data{"instances": instances, "adjustment": trigger.Adjustment})
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to compute new app instances"
		return -1, err
	}

	schedule, err := s.scalingEngineDB.GetActiveSchedule(appId)
	if err != nil {
		logger.Error("failed-get-active-schedule", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to get active schedule"
		return -1, err
	}

	var instanceMin, instanceMax int

	if schedule != nil {
		instanceMin = schedule.InstanceMin
		instanceMax = schedule.InstanceMax
	} else {
		var policy *models.ScalingPolicy
		policy, err = s.policyDB.GetAppPolicy(appId)
		if err != nil {
			logger.Error("failed-get-app-policy", err)
			history.Status = models.ScalingStatusFailed
			history.Error = "failed to get scaling policy"
			return -1, err
		} else {
			instanceMin = policy.InstanceMin
			instanceMax = policy.InstanceMax
		}
	}

	if newInstances < instanceMin {
		newInstances = instanceMin
		history.Message = fmt.Sprintf("limited by min instances %d", instanceMin)
	} else if newInstances > instanceMax {
		newInstances = instanceMax
		history.Message = fmt.Sprintf("limited by max instances %d", instanceMax)
	}
	history.NewInstances = newInstances

	if newInstances == instances {
		history.Status = models.ScalingStatusIgnored
		return newInstances, nil
	}

	err = s.cfClient.SetAppInstances(appId, newInstances)
	if err != nil {
		logger.Error("failed-to-set-app-instances", err, lager.Data{"newInstances": newInstances})
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to set app instances"
		return -1, err
	}

	history.Status = models.ScalingStatusSucceeded

	err = s.scalingEngineDB.UpdateScalingCooldownExpireTime(appId, now.Add(trigger.CoolDown()).UnixNano())
	if err != nil {
		logger.Error("failed-to-update-scaling-cool-down-expire-time", err, lager.Data{"newInstances": newInstances})
	}

	return newInstances, nil
}

func (s *scalingEngine) ComputeNewInstances(currentInstances int, adjustment string) (int, error) {
	var newInstances int
	if strings.HasSuffix(adjustment, "%") {
		percentage, err := strconv.ParseFloat(strings.TrimSuffix(adjustment, "%"), 32)
		if err != nil {
			s.logger.Error("failed-to-parse-percentage", err, lager.Data{"adjustment": adjustment})
			return -1, err
		}
		newInstances = int(float64(currentInstances)*(1+percentage/100) + 0.5)
	} else {
		step, err := strconv.ParseInt(adjustment, 10, 32)
		if err != nil {
			s.logger.Error("failed-to-parse-step-adjustment", err, lager.Data{"adjustment": adjustment})
			return -1, err
		}
		newInstances = int(step) + currentInstances
	}

	return newInstances, nil
}

func (s *scalingEngine) SetActiveSchedule(appId string, schedule *models.ActiveSchedule) error {
	logger := s.logger.WithData(lager.Data{"appId": appId, "schedule": schedule})

	s.appLock.GetLock(appId).Lock()
	defer s.appLock.GetLock(appId).Unlock()

	currentSchedule, err := s.scalingEngineDB.GetActiveSchedule(appId)
	if err != nil {
		logger.Error("failed-to-get-existing-active-schedule-from-database", err)
		return err
	}

	if currentSchedule != nil {
		if schedule.ScheduleId == currentSchedule.ScheduleId {
			logger.Info("set-active-schedule", lager.Data{"message": "duplicate request to set active schedule"})
			return nil
		} else {
			logger.Info("set-active-schedule", lager.Data{"message": "an active schedule exists in database", "currentSchedule": currentSchedule})
		}
	}

	err = s.scalingEngineDB.SetActiveSchedule(appId, schedule)
	if err != nil {
		logger.Error("failed-to-set-active-schedule-in-database", err)
		return err
	}

	now := s.clock.Now()
	history := &models.AppScalingHistory{
		AppId:        appId,
		Timestamp:    now.UnixNano(),
		ScalingType:  models.ScalingTypeSchedule,
		OldInstances: -1,
		NewInstances: -1,
		Reason:       getScheduledScalingReason(schedule),
	}
	defer s.scalingEngineDB.SaveScalingHistory(history)

	instances, err := s.cfClient.GetAppInstances(appId)
	if err != nil {
		logger.Error("failed-to-get-app-instances", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to get app instances"
		return err
	}
	history.OldInstances = instances

	instanceMin := schedule.InstanceMinInitial
	if schedule.InstanceMin > instanceMin {
		instanceMin = schedule.InstanceMin
	}

	newInstances := instances
	if newInstances < instanceMin {
		newInstances = instanceMin
		history.Message = fmt.Sprintf("limited by min instances %d", instanceMin)
	} else if newInstances > schedule.InstanceMax {
		newInstances = schedule.InstanceMax
		history.Message = fmt.Sprintf("limited by max instances %d", instanceMin)
	}

	history.NewInstances = newInstances

	if newInstances == instances {
		history.Status = models.ScalingStatusIgnored
		return nil
	}

	err = s.cfClient.SetAppInstances(appId, newInstances)
	if err != nil {
		logger.Error("failed-to-set-app-instances", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to set app instances"
		return err
	}
	history.Status = models.ScalingStatusSucceeded
	return nil
}

func (s *scalingEngine) RemoveActiveSchedule(appId string, scheduleId string) error {
	logger := s.logger.WithData(lager.Data{"appId": appId, "scheduleId": scheduleId})

	s.appLock.GetLock(appId).Lock()
	defer s.appLock.GetLock(appId).Unlock()

	currentSchedule, err := s.scalingEngineDB.GetActiveSchedule(appId)
	if err != nil {
		logger.Error("failed-to-get-existing-active-schedule-from-database", err)
		return err
	}

	if (currentSchedule == nil) || (currentSchedule.ScheduleId != scheduleId) {
		err = &ActiveScheduleNotFoundError{}
		logger.Error("failed-to-remove-active-schedule", err)
		return err
	}

	err = s.scalingEngineDB.RemoveActiveSchedule(appId)
	if err != nil {
		logger.Error("failed-to-remove-active-schedule-from-database", err)
		return err
	}

	now := s.clock.Now()
	history := &models.AppScalingHistory{
		AppId:        appId,
		Timestamp:    now.UnixNano(),
		ScalingType:  models.ScalingTypeSchedule,
		OldInstances: -1,
		NewInstances: -1,
		Reason:       "schedule ends",
	}
	defer s.scalingEngineDB.SaveScalingHistory(history)

	instances, err := s.cfClient.GetAppInstances(appId)
	if err != nil {
		logger.Error("failed-to-get-app-instances", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to get app instances"
		return err
	}
	history.OldInstances = instances

	policy, err := s.policyDB.GetAppPolicy(appId)
	if err != nil {
		logger.Error("failed-to-get-app-policy", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to get app policy"
		return err
	}

	if policy == nil {
		history.Status = models.ScalingStatusIgnored
		return nil
	}

	newInstances := instances
	if newInstances < policy.InstanceMin {
		newInstances = policy.InstanceMin
		history.Message = fmt.Sprintf("limited by min instances %d", policy.InstanceMin)
	} else if newInstances > policy.InstanceMax {
		newInstances = policy.InstanceMax
		history.Message = fmt.Sprintf("limited by max instances %d", policy.InstanceMax)
	}

	history.NewInstances = newInstances

	if newInstances == instances {
		history.Status = models.ScalingStatusIgnored
		return nil
	}

	err = s.cfClient.SetAppInstances(appId, newInstances)
	if err != nil {
		logger.Error("failed-to-set-app-instances", err)
		history.Status = models.ScalingStatusFailed
		history.Error = "failed to set app instances"
		return err
	}
	history.Status = models.ScalingStatusSucceeded
	return nil
}

func getDynamicScalingReason(trigger *models.Trigger) string {
	return fmt.Sprintf("%s instance(s) because %s %s %d for %d seconds",
		trigger.Adjustment,
		trigger.MetricType,
		trigger.Operator,
		trigger.Threshold,
		trigger.BreachDurationSeconds)
}

func getScheduledScalingReason(schedule *models.ActiveSchedule) string {
	return fmt.Sprintf("schedule starts with instance min %d, instance max %d and instance min initial %d",
		schedule.InstanceMin, schedule.InstanceMax, schedule.InstanceMinInitial)
}
