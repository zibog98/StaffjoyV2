package main

import (
	_ "github.com/go-sql-driver/mysql"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	pb "v2.staffjoy.com/company"
	"v2.staffjoy.com/crypto"
	"v2.staffjoy.com/frontcache"
	"v2.staffjoy.com/helpers"
)

func (s *companyServer) CreateCompany(ctx context.Context, req *pb.CreateCompanyRequest) (*pb.Company, error) {
	md, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "failed to authorize")
	// }
	// switch authz {
	// case auth.AuthorizationSupportUser:
	// case auth.AuthorizationWWWService:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "you do not have access to this service")
	// }

	// sanitization
	req.DefaultDayWeekStarts, err = sanitizeDayOfWeek(req.DefaultDayWeekStarts)
	if err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "Invalid DefaultDayWeekStarts")
	}
	if err = validTimezone(req.DefaultTimezone); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "invalid timezone")
	}
	if len(req.Name) == 0 {
		return nil, grpc.Errorf(codes.InvalidArgument, "name is required")
	}

	uuid, err := crypto.NewUUID()
	if err != nil {
		return nil, s.internalError(err, "cannot generate a uuid")
	}

	c := &pb.Company{Uuid: uuid.String(), Name: req.Name, DefaultDayWeekStarts: req.DefaultDayWeekStarts, DefaultTimezone: req.DefaultTimezone, Version: 0}
	if err = s.dbMap.Insert(c); err != nil {
		return nil, s.internalError(err, "could not create company")
	}
	al := newAuditEntry(md, "company", c.Uuid, c.Uuid, "")
	al.UpdatedContents = c
	al.Log(logger, "created company")
	go helpers.TrackEventFromMetadata(md, "company_created")

	if s.use_caching {
		s.company_lock.Lock()
		s.company_cache[c.Uuid] = c
		s.company_lock.Unlock()
	}

	return c, nil
}

func (s *companyServer) ListCompanyRows(ctx context.Context, req *pb.CompanyListRequest) (*pb.RowsOfCompany, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	res := &pb.RowsOfCompany{}
	rows, err := s.db.Query("select uuid from company limit ? offset ?", req.Limit, req.Offset)
	if err != nil {
		return nil, s.internalError(err, "Unable to query database")
	}

	for rows.Next() {
		var uuid string
		if err := rows.Scan(&uuid); err != nil {
			return nil, s.internalError(err, "Error scanning database")
		}
		res.CompanyUuid = append(res.CompanyUuid, uuid)
	}
	return res, nil
}

func (s *companyServer) ListCompanies(ctx context.Context, req *pb.CompanyListRequest) (*pb.CompanyList, error) {
	_, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "Failed to authorize")
	// }
	// switch authz {
	// case auth.AuthorizationSupportUser:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	if req.Limit <= 0 {
		req.Limit = 20
	}

	res := &pb.CompanyList{Limit: req.Limit, Offset: req.Offset}
	rows, err := s.db.Query("select uuid from company limit ? offset ?", req.Limit, req.Offset)
	if err != nil {
		return nil, s.internalError(err, "Unable to query database")
	}

	for rows.Next() {
		r := &pb.GetCompanyRequest{}
		if err := rows.Scan(&r.Uuid); err != nil {
			return nil, s.internalError(err, "Error scanning database")
		}

		// TODO - can we parallelize this, and maybe be hitting redis?
		var c *pb.Company
		if c, err = s.GetCompany(ctx, r); err != nil {
			return nil, err
		}
		res.Companies = append(res.Companies, *c)
	}
	return res, nil
}

func (s *companyServer) GetCompany(ctx context.Context, req *pb.GetCompanyRequest) (*pb.Company, error) {
	// defer helpers.Duration(helpers.Track("GetCompany"))
	if s.use_caching {
		if res, ok := s.company_cache[req.Uuid]; ok {
			s.logger.Info("get company cache hit [company uuid:" + req.Uuid + "]")
			return res, nil
		} else {
			s.logger.Info("get company cache miss [company uuid:" + req.Uuid + "]")
		}
	}
	_, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "Failed to authorize")
	// }

	// switch authz {
	// case auth.AuthorizationAccountService:
	// case auth.AuthorizationBotService:
	// case auth.AuthorizationWhoamiService:
	// case auth.AuthorizationAuthenticatedUser:
	// 	if err = s.PermissionCompanyDirectory(md, req.Uuid); err != nil {
	// 		return nil, err
	// 	}
	// case auth.AuthorizationSupportUser:
	// case auth.AuthorizationWWWService:
	// case auth.AuthorizationICalService:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	obj, err := s.dbMap.Get(pb.Company{}, req.Uuid)
	if err != nil {
		return nil, s.internalError(err, "unable to query database")
	} else if obj == nil {
		return nil, grpc.Errorf(codes.NotFound, "company not found")
	}

	if s.use_caching {
		s.company_lock.Lock()
		s.company_cache[req.Uuid] = obj.(*pb.Company)
		s.company_lock.Unlock()
	}
	return obj.(*pb.Company), nil
}

func (s *companyServer) UpdateCompany(ctx context.Context, req *pb.Company) (*pb.Company, error) {
	defer helpers.Duration(helpers.Track("UpdateCompany"))
	md, _, err := getAuth(ctx)
	// switch authz {
	// case auth.AuthorizationAuthenticatedUser:
	// 	if err = s.PermissionCompanyAdmin(md, req.Uuid); err != nil {
	// 		return nil, err
	// 	}
	// case auth.AuthorizationSupportUser:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	// sanitization
	if req.DefaultDayWeekStarts, err = sanitizeDayOfWeek(req.DefaultDayWeekStarts); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "Invalid DefaultDayWeekStarts")
	}
	if err = validTimezone(req.DefaultTimezone); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "Invalid timezone")
	}
	c, err := s.GetCompany(ctx, &pb.GetCompanyRequest{Uuid: req.Uuid})
	if err != nil {
		return nil, err
	}
	if _, err := s.dbMap.Update(req); err != nil {
		return nil, s.internalError(err, "Could not update the company")
	}

	al := newAuditEntry(md, "company", req.Uuid, req.Uuid, "")
	al.OriginalContents = c
	al.UpdatedContents = req
	al.Log(logger, "updated company")
	go helpers.TrackEventFromMetadata(md, "company_updated")

	if s.use_caching {
		if c, ok := s.company_cache[req.Uuid]; ok {
			s.company_lock.Lock()
			s.company_cache[req.Uuid] = req
			if !s.use_callback {
				s.company_cache[req.Uuid].Version = c.Version + 1
			}
			s.company_lock.Unlock()
			s.logger.Info("update company cache [orig:" + req.Uuid + "]")

			if s.use_callback {
				frontcacheClient, close, err := frontcache.NewClient()
				if err != nil {
					return nil, s.internalError(err, "unable to init frontcache connection")
				}
				defer close()

				_, err = frontcacheClient.InvalidateCompanyCache(ctx, &frontcache.InvalidateCompanyCacheRequest{CompanyUuid: req.Uuid})
				if err != nil {
					return nil, s.internalError(err, "error invalidate FrontCache company cache")
				}
			}
		}
	}

	return req, nil
}

func (s *companyServer) GetCompanyVersion(ctx context.Context, req *pb.GetCompanyVersionRequest) (*pb.CompanyVersion, error) {
	if res, ok := s.company_cache[req.Uuid]; ok {
		return &pb.CompanyVersion{CompanyVer: res.Version}, nil
	}
	return &pb.CompanyVersion{CompanyVer: 0}, nil
	// return nil, fmt.Errorf("GetCompanyVersion not found req uuid")
}
