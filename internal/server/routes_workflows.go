package server

// routes_workflows.go — the HTTP half of the WI-owned workflow (aihub#708
// Batch 2A): GET/PUT /v1/work_items/:id/workflow plus the four
// controller-facing actions under it (start, result, approve, repair).
//
// Authorization per route, matching the conventions the neighbouring work-item
// routes already use (checkProjectAccess + writeError + hideNotFound):
//
//	GET      project VIEWER — the workflow view is work-item state; the caller
//	         can already read the work item, and the view carries references
//	         and progress, never skill content (that stays behind the
//	         registry's own access checks).
//	PUT      project WRITER; the domain layer then applies the stricter
//	         contract-tier matrix (reporter / maintainer / admin, open status
//	         only) on the locked row — the same gate goal and wi_type edits
//	         use, so the two "what this work item is" edits cannot disagree.
//	start/result/repair/reconcile  project WRITER + the CURRENT attempt's
//	         credentials (attempt_id + claim_epoch + session_secret), exactly like
//	         the existing step routes. No route on this file authorizes anything
//	         from what the body claims; the body carries credentials, the
//	         server derives the rest. The reconcile route is the explicit
//	         controller-reconcile transition (aihub#708 B3): it supersedes a
//	         dead attempt's open invocations, and the dead-ness of the named
//	         attempt is verified against that attempt's own row under the work
//	         item lock — never taken from the request.
//	approve  project WRITER + an authenticated HUMAN user_type. A machine
//	         credential cannot approve, and there is no actor field on the
//	         wire at all — the actor is the authenticated principal.

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// workflowBodyLimit bounds every workflow request body. A revision carries one
// step spec per step (ids, model candidates, params against bounded schemas);
// 4 MiB is an order of magnitude above a legitimate flow and deliberately
// below the registry's 16 MiB envelope, whose bundles carry whole file
// contents.
const workflowBodyLimit = 4 << 20

// RegisterWorkflowRoutes exposes the WI workflow endpoints.
func RegisterWorkflowRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	v1.GET("/work_items/:id/workflow", handleGetWorkflow(pool))
	v1.PUT("/work_items/:id/workflow", handlePutWorkflow(pool))
	v1.POST("/work_items/:id/workflow/start", handleWorkflowStart(pool))
	v1.POST("/work_items/:id/workflow/result", handleWorkflowResult(pool))
	v1.POST("/work_items/:id/workflow/approve", handleWorkflowApprove(pool))
	v1.POST("/work_items/:id/workflow/repair", handleWorkflowRepair(pool))
	v1.POST("/work_items/:id/workflow/reconcile", handleWorkflowReconcile(pool))
}

// workflowCallerRecord builds the registry-authorization view of the caller
// (the same UserRecord shape the registry paths authorize with), so skill
// resolution inside pin/revise applies the caller's true scope — a scoped API
// key resolves shares through its project only, never the wider public view.
func workflowCallerRecord(u *UserContext) *domain.UserRecord {
	if u == nil {
		return nil
	}
	return &domain.UserRecord{ID: u.UserID, Role: u.Role, ProjectScope: u.ProjectScope}
}

// bindWorkflowBody binds one workflow request body under a hard size cap.
// echo's Bind has no limit of its own and the global middleware sets none for
// JSON bodies, so the cap lives here beside the routes that need it — the same
// placement the skill registry chose for its envelope.
func bindWorkflowBody(c echo.Context, dst any) *domain.AihubError {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, workflowBodyLimit)
	if err := c.Bind(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return domain.NewErr(domain.ErrPayloadTooLarge, fmt.Sprintf(
				"workflow request body exceeds %d bytes; nothing was applied", workflowBodyLimit))
		}
		return domain.NewErr(domain.ErrBadRequest, "invalid request body")
	}
	return nil
}

func handleGetWorkflow(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		// Resolve under the same visibility rule as reading the work item:
		// the loader's NOT_FOUND goes through hideNotFound so an invisible
		// work item and a missing one answer identically.
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "viewer"); err != nil {
			return err
		}

		workflow, aerr := domain.GetWorkItemWorkflow(ctx, pool, wi.ID)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, workflow)
	}
}

func handlePutWorkflow(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		var req domain.UpdateWorkItemWorkflowRequest
		if err := bindWorkflowBody(c, &req); err != nil {
			return writeError(c, err)
		}

		out, aerr := domain.UpdateWorkItemWorkflow(ctx, pool, wi.ID,
			workflowCallerRecord(u), u.UserID, u.Role, u.ProjectRoles, req)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handleWorkflowStart(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		var req domain.StartWorkflowStepRequest
		if err := bindWorkflowBody(c, &req); err != nil {
			return writeError(c, err)
		}

		out, aerr := domain.StartWorkflowStep(ctx, pool, wi.ID, req)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handleWorkflowResult(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		var req domain.RecordWorkflowResultRequest
		if err := bindWorkflowBody(c, &req); err != nil {
			return writeError(c, err)
		}

		out, aerr := domain.RecordWorkflowResult(ctx, pool, wi.ID, req)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handleWorkflowApprove(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		var req domain.ApproveWorkflowRequest
		if err := bindWorkflowBody(c, &req); err != nil {
			return writeError(c, err)
		}

		// The human gate: user_type from the AUTHENTICATED context, never a
		// body field. The domain repeats the check so the policy is testable
		// without HTTP, but this is where a machine credential first stops.
		out, aerr := domain.ApproveWorkflowStep(ctx, pool, wi.ID,
			workflowCallerRecord(u), u.DisplayName, u.UserType, req)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handleWorkflowRepair(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		var req domain.AuthorizeWorkflowRepairRequest
		if err := bindWorkflowBody(c, &req); err != nil {
			return writeError(c, err)
		}

		out, aerr := domain.AuthorizeWorkflowRepair(ctx, pool, wi.ID, req)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handleWorkflowReconcile(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		wi, err := domain.GetWorkItem(ctx, pool, wiID)
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		var req domain.ReconcileWorkflowInvocationsRequest
		if err := bindWorkflowBody(c, &req); err != nil {
			return writeError(c, err)
		}

		out, aerr := domain.ReconcileWorkflowInvocations(ctx, pool, wi.ID, req)
		if aerr != nil {
			return writeError(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}
