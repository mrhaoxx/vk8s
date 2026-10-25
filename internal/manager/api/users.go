package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/manager/auth"
	"vk8s.io/vk8s/internal/names"
)

// createUserRequest POST /api/v1/users。
type createUserRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	// Password 非空则创建静态登录。
	Password string `json:"password"`
}

// patchUserRequest PATCH /api/v1/users/{name}。
type patchUserRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
	Email       *string `json:"email,omitempty"`
	Role        *string `json:"role,omitempty"`
	Disabled    *bool   `json:"disabled,omitempty"`
}

// passwordRequest POST /api/v1/users/{name}/password。
type passwordRequest struct {
	Password string `json:"password"`
}

func parseRole(s string) (v1alpha1.UserRole, error) {
	switch v1alpha1.UserRole(s) {
	case "":
		return v1alpha1.UserRoleUser, nil
	case v1alpha1.UserRoleUser, v1alpha1.UserRoleAdmin:
		return v1alpha1.UserRole(s), nil
	default:
		return "", fmt.Errorf("role %q must be user|admin", s)
	}
}

// listUsers GET /api/v1/users。
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	list := &v1alpha1.UserList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]UserView, 0, len(list.Items))
	for i := range list.Items {
		items = append(items, userView(&list.Items[i]))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, listResponse[UserView]{Items: items})
}

// createUser POST /api/v1/users。
func (s *Server) createUser(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	var req createUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	audit.Annotate(r.Context()).Target = name
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, "name: "+strings.Join(errs, "; "))
		return
	}
	role, err := parseRole(req.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var hash []byte
	if req.Password != "" {
		hash, err = auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.UserSpec{
			DisplayName: strings.TrimSpace(req.DisplayName),
			Email:       strings.TrimSpace(req.Email),
			Role:        role,
		},
	}
	if user.Spec.DisplayName == "" {
		user.Spec.DisplayName = name
	}
	if hash != nil {
		user.Spec.Auth.Static = &v1alpha1.StaticAuth{PasswordSecretRef: corev1.LocalObjectReference{Name: names.UserPasswordSecret(name)}}
	}
	if err := s.Client.Create(r.Context(), user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("user %q already exists", name))
			return
		}
		s.internalError(w, r, err)
		return
	}
	if hash != nil {
		if err := s.writePassword(r, user, hash); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, userView(user))
}

// patchUser PATCH /api/v1/users/{name}。不允许改自己的角色或禁用自己，避免把最后一个管理员锁在门外。
func (s *Server) patchUser(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	var req patchUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.PathValue("name")
	var role v1alpha1.UserRole
	if req.Role != nil {
		var err error
		if role, err = parseRole(*req.Role); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if name == me.Name && ((req.Role != nil && role != me.Spec.Role) || (req.Disabled != nil && *req.Disabled)) {
		writeError(w, http.StatusBadRequest, "you cannot change your own role or disable yourself")
		return
	}
	user := &v1alpha1.User{}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := s.Client.Get(r.Context(), types.NamespacedName{Name: name}, user); err != nil {
			return err
		}
		if req.DisplayName != nil {
			user.Spec.DisplayName = strings.TrimSpace(*req.DisplayName)
		}
		if req.Email != nil {
			user.Spec.Email = strings.TrimSpace(*req.Email)
		}
		if req.Role != nil {
			user.Spec.Role = role
		}
		if req.Disabled != nil {
			user.Spec.Disabled = *req.Disabled
		}
		return s.Client.Update(r.Context(), user)
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userView(user))
}

// setPassword POST /api/v1/users/{name}/password：写 bcrypt Secret，并确保 spec.auth.static 指向它。
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	audit.Annotate(r.Context()).SkipBody = true // 体里是新密码
	var req passwordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.PathValue("name")
	user := &v1alpha1.User{}
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := s.Client.Get(r.Context(), types.NamespacedName{Name: name}, user); err != nil {
			return err
		}
		if user.Spec.Auth.Static != nil && user.Spec.Auth.Static.PasswordSecretRef.Name != "" {
			return nil
		}
		user.Spec.Auth.Static = &v1alpha1.StaticAuth{PasswordSecretRef: corev1.LocalObjectReference{Name: names.UserPasswordSecret(name)}}
		return s.Client.Update(r.Context(), user)
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if err := s.writePassword(r, user, hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writePassword 写用户密码 Secret（ownerReference → User）。
func (s *Server) writePassword(r *http.Request, user *v1alpha1.User, hash []byte) error {
	return auth.WritePasswordSecret(r.Context(), s.Client, s.Config.SystemNamespace, user, hash)
}
