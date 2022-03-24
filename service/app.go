package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"

	"github.com/gilcrest/go-api-basic/datastore"
	"github.com/gilcrest/go-api-basic/datastore/appstore"
	"github.com/gilcrest/go-api-basic/domain/app"
	"github.com/gilcrest/go-api-basic/domain/audit"
	"github.com/gilcrest/go-api-basic/domain/errs"
	"github.com/gilcrest/go-api-basic/domain/org"
	"github.com/gilcrest/go-api-basic/domain/person"
	"github.com/gilcrest/go-api-basic/domain/secure"
	"github.com/gilcrest/go-api-basic/domain/user"
)

// appAudit is the combination of a domain App and its audit data
type appAudit struct {
	App         app.App
	SimpleAudit audit.SimpleAudit
}

// CreateAppRequest is the request struct for Creating an App
type CreateAppRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AppResponse is the response struct for an App
type AppResponse struct {
	ExternalID          string           `json:"external_id"`
	Name                string           `json:"name"`
	Description         string           `json:"description"`
	CreateAppExtlID     string           `json:"create_app_extl_id"`
	CreateUsername      string           `json:"create_username"`
	CreateUserFirstName string           `json:"create_user_first_name"`
	CreateUserLastName  string           `json:"create_user_last_name"`
	CreateDateTime      string           `json:"create_date_time"`
	UpdateAppExtlID     string           `json:"update_app_extl_id"`
	UpdateUsername      string           `json:"update_username"`
	UpdateUserFirstName string           `json:"update_user_first_name"`
	UpdateUserLastName  string           `json:"update_user_last_name"`
	UpdateDateTime      string           `json:"update_date_time"`
	APIKeys             []APIKeyResponse `json:"api_keys"`
}

// APIKeyResponse is the response fields for an API key
type APIKeyResponse struct {
	Key              string `json:"key"`
	DeactivationDate string `json:"deactivation_date"`
}

// newAPIKeyResponse initializes an APIKeyResponse. The app.APIKey is
// decrypted and set to the Key field as part of initialization.
func newAPIKeyResponse(key app.APIKey) APIKeyResponse {
	return APIKeyResponse{Key: key.Key(), DeactivationDate: key.DeactivationDate().String()}
}

// newAppResponse initializes an AppResponse given an app.App
func newAppResponse(aa appAudit) AppResponse {
	var keys []APIKeyResponse
	for _, key := range aa.App.APIKeys {
		akr := newAPIKeyResponse(key)
		keys = append(keys, akr)
	}
	return AppResponse{
		ExternalID:          aa.App.ExternalID.String(),
		Name:                aa.App.Name,
		Description:         aa.App.Description,
		CreateAppExtlID:     aa.SimpleAudit.First.App.ExternalID.String(),
		CreateUsername:      aa.SimpleAudit.First.User.Username,
		CreateUserFirstName: aa.SimpleAudit.First.User.Profile.FirstName,
		CreateUserLastName:  aa.SimpleAudit.First.User.Profile.LastName,
		CreateDateTime:      aa.SimpleAudit.First.Moment.Format(time.RFC3339),
		UpdateAppExtlID:     aa.SimpleAudit.Last.App.ExternalID.String(),
		UpdateUsername:      aa.SimpleAudit.Last.User.Username,
		UpdateUserFirstName: aa.SimpleAudit.Last.User.Profile.FirstName,
		UpdateUserLastName:  aa.SimpleAudit.Last.User.Profile.LastName,
		UpdateDateTime:      aa.SimpleAudit.Last.Moment.Format(time.RFC3339),
		APIKeys:             keys,
	}
}

// AppService is a service for creating an App
type AppService struct {
	Datastorer            Datastorer
	RandomStringGenerator CryptoRandomGenerator
	EncryptionKey         *[32]byte
}

// Create is used to create an App
func (s AppService) Create(ctx context.Context, r *CreateAppRequest, adt audit.Audit) (AppResponse, error) {
	var (
		a   app.App
		err error
	)
	a.ID = uuid.New()
	a.ExternalID = secure.NewID()
	a.Org = adt.App.Org
	a.Name = r.Name
	a.Description = r.Description

	keyDeactivation := time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)
	err = a.AddNewKey(s.RandomStringGenerator, s.EncryptionKey, keyDeactivation)
	if err != nil {
		return AppResponse{}, err
	}

	createAppParams := appstore.CreateAppParams{
		AppID:           a.ID,
		OrgID:           a.Org.ID,
		AppExtlID:       a.ExternalID.String(),
		AppName:         a.Name,
		AppDescription:  a.Description,
		CreateAppID:     adt.App.ID,
		CreateUserID:    datastore.NewNullUUID(adt.User.ID),
		CreateTimestamp: adt.Moment,
		UpdateAppID:     adt.App.ID,
		UpdateUserID:    datastore.NewNullUUID(adt.User.ID),
		UpdateTimestamp: adt.Moment,
	}

	// start db txn using pgxpool
	var tx pgx.Tx
	tx, err = s.Datastorer.BeginTx(ctx)
	if err != nil {
		return AppResponse{}, err
	}

	// create app database record using appstore
	var rowsAffected int64
	rowsAffected, err = appstore.New(tx).CreateApp(ctx, createAppParams)
	if err != nil {
		return AppResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, err))
	}

	if rowsAffected != 1 {
		return AppResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, fmt.Sprintf("rows affected should be 1, actual: %d", rowsAffected)))
	}

	for _, key := range a.APIKeys {

		createAppAPIKeyParams := appstore.CreateAppAPIKeyParams{
			ApiKey:          key.Ciphertext(),
			AppID:           a.ID,
			DeactvDate:      key.DeactivationDate(),
			CreateAppID:     adt.App.ID,
			CreateUserID:    datastore.NewNullUUID(adt.User.ID),
			CreateTimestamp: adt.Moment,
			UpdateAppID:     adt.App.ID,
			UpdateUserID:    datastore.NewNullUUID(adt.User.ID),
			UpdateTimestamp: adt.Moment,
		}

		// create app API key database record using appstore
		var apiKeyRowsAffected int64
		apiKeyRowsAffected, err = appstore.New(tx).CreateAppAPIKey(ctx, createAppAPIKeyParams)
		if err != nil {
			return AppResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, err))
		}

		if apiKeyRowsAffected != 1 {
			return AppResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, fmt.Sprintf("rows affected should be 1, actual: %d", apiKeyRowsAffected)))
		}

	}

	// commit db txn using pgxpool
	err = s.Datastorer.CommitTx(ctx, tx)
	if err != nil {
		return AppResponse{}, err
	}

	return newAppResponse(appAudit{App: a, SimpleAudit: audit.SimpleAudit{First: adt, Last: adt}}), nil
}

// UpdateAppRequest is the request struct for Updating an App
type UpdateAppRequest struct {
	ExternalID  string
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Update is used to update an App. API Keys for an App cannot be updated.
func (s AppService) Update(ctx context.Context, r *UpdateAppRequest, adt audit.Audit) (AppResponse, error) {

	var err error

	// retrieve existing Org
	var aa appAudit
	aa, err = findAppByExternalIDWithAudit(ctx, s.Datastorer.Pool(), r.ExternalID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return AppResponse{}, errs.E(errs.Validation, "No app exists for the given external ID")
		}
		return AppResponse{}, errs.E(errs.Database, err)
	}
	// overwrite Last audit with the current audit
	aa.SimpleAudit.Last = adt

	// override fields with data from request
	aa.App.Name = r.Name
	aa.App.Description = r.Description

	updateAppParams := appstore.UpdateAppParams{
		AppName:         aa.App.Name,
		AppDescription:  aa.App.Description,
		UpdateAppID:     adt.App.ID,
		UpdateUserID:    datastore.NewNullUUID(adt.User.ID),
		UpdateTimestamp: adt.Moment,
		AppID:           aa.App.ID,
	}

	// start db txn using pgxpool
	var tx pgx.Tx
	tx, err = s.Datastorer.BeginTx(ctx)
	if err != nil {
		return AppResponse{}, err
	}

	var rowsAffected int64
	rowsAffected, err = appstore.New(tx).UpdateApp(ctx, updateAppParams)
	if err != nil {
		return AppResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, err))
	}

	if rowsAffected != 1 {
		return AppResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, fmt.Sprintf("rows affected should be 1, actual: %d", rowsAffected)))
	}

	// commit db txn using pgxpool
	err = s.Datastorer.CommitTx(ctx, tx)
	if err != nil {
		return AppResponse{}, err
	}

	return newAppResponse(aa), nil
}

// Delete is used to delete an App
func (s AppService) Delete(ctx context.Context, extlID string) (DeleteResponse, error) {

	// retrieve existing Org
	a, err := findAppByExternalID(ctx, s.Datastorer.Pool(), extlID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return DeleteResponse{}, errs.E(errs.Validation, "No app exists for the given external ID")
		}
		return DeleteResponse{}, errs.E(errs.Database, err)
	}

	// start db txn using pgxpool
	var tx pgx.Tx
	tx, err = s.Datastorer.BeginTx(ctx)
	if err != nil {
		return DeleteResponse{}, err
	}

	// one-to-many API keys can be associated with an App. This will
	// delete them all.
	var apiKeysRowsAffected int64
	apiKeysRowsAffected, err = appstore.New(tx).DeleteAppAPIKeys(ctx, a.ID)
	if err != nil {
		return DeleteResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, err))
	}

	if apiKeysRowsAffected < 1 {
		return DeleteResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, fmt.Sprintf("rows affected should be at least 1, actual: %d", apiKeysRowsAffected)))
	}

	var rowsAffected int64
	rowsAffected, err = appstore.New(tx).DeleteApp(ctx, a.ID)
	if err != nil {
		return DeleteResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, err))
	}

	if rowsAffected != 1 {
		return DeleteResponse{}, s.Datastorer.RollbackTx(ctx, tx, errs.E(errs.Database, fmt.Sprintf("rows affected should be 1, actual: %d", rowsAffected)))
	}

	// commit db txn using pgxpool
	err = s.Datastorer.CommitTx(ctx, tx)
	if err != nil {
		return DeleteResponse{}, err
	}

	response := DeleteResponse{
		ExternalID: extlID,
		Deleted:    true,
	}

	return response, nil
}

func findAppByExternalID(ctx context.Context, dbtx DBTX, extlID string) (app.App, error) {
	row, err := appstore.New(dbtx).FindAppByExternalID(ctx, extlID)
	if err != nil {
		return app.App{}, errs.E(errs.Database, err)
	}

	a := app.App{
		ID:         row.AppID,
		ExternalID: secure.MustParseIdentifier(row.AppExtlID),
		Org: org.Org{
			ID:          row.OrgID,
			ExternalID:  secure.MustParseIdentifier(row.OrgExtlID),
			Name:        row.OrgName,
			Description: row.OrgDescription,
			Kind: org.Kind{
				ID:          row.OrgKindID,
				ExternalID:  row.OrgKindExtlID,
				Description: row.OrgKindDesc,
			},
		},
		Name:        row.AppName,
		Description: row.AppDescription,
		APIKeys:     nil,
	}

	return a, nil
}

// findAppByExternalIDWithAudit retrieves App data from the datastore given a unique external ID.
// This data is then hydrated into the app.App struct along with the simple audit struct
func findAppByExternalIDWithAudit(ctx context.Context, dbtx DBTX, extlID string) (appAudit, error) {
	var (
		row appstore.FindAppByExternalIDWithAuditRow
		err error
	)

	row, err = appstore.New(dbtx).FindAppByExternalIDWithAudit(ctx, extlID)
	if err != nil {
		return appAudit{}, errs.E(errs.Database, err)
	}

	a := app.App{
		ID:         row.AppID,
		ExternalID: secure.MustParseIdentifier(row.AppExtlID),
		Org: org.Org{
			ID:          row.OrgID,
			ExternalID:  secure.MustParseIdentifier(row.OrgExtlID),
			Name:        row.OrgName,
			Description: row.OrgDescription,
			Kind: org.Kind{
				ID:          row.OrgKindID,
				ExternalID:  row.OrgKindExtlID,
				Description: row.OrgKindDesc,
			},
		},
		Name:        row.AppName,
		Description: row.AppDescription,
		APIKeys:     nil,
	}

	sa := audit.SimpleAudit{
		First: audit.Audit{
			App: app.App{
				ID:          row.CreateAppID,
				ExternalID:  secure.MustParseIdentifier(row.CreateAppExtlID),
				Org:         org.Org{ID: row.CreateAppOrgID},
				Name:        row.CreateAppName,
				Description: row.CreateAppDescription,
				APIKeys:     nil,
			},
			User: user.User{
				ID:       row.CreateUserID.UUID,
				Username: row.CreateUsername,
				Org:      org.Org{ID: row.CreateUserOrgID},
				Profile: person.Profile{
					FirstName: row.CreateUserFirstName,
					LastName:  row.CreateUserLastName,
				},
			},
			Moment: row.CreateTimestamp,
		},
		Last: audit.Audit{
			App: app.App{
				ID:          row.UpdateAppID,
				ExternalID:  secure.MustParseIdentifier(row.UpdateAppExtlID),
				Org:         org.Org{ID: row.UpdateAppOrgID},
				Name:        row.UpdateAppName,
				Description: row.UpdateAppDescription,
				APIKeys:     nil,
			},
			User: user.User{
				ID:       row.UpdateUserID.UUID,
				Username: row.UpdateUsername,
				Org:      org.Org{ID: row.UpdateUserOrgID},
				Profile: person.Profile{
					FirstName: row.UpdateUserFirstName,
					LastName:  row.UpdateUserLastName,
				},
			},
			Moment: row.UpdateTimestamp,
		},
	}

	return appAudit{App: a, SimpleAudit: sa}, nil
}
