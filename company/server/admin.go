package main

import (
	"github.com/golang/protobuf/ptypes/empty"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	pb "v2.staffjoy.com/company"
	"v2.staffjoy.com/frontcache"
	"v2.staffjoy.com/helpers"
)

func (s *companyServer) ListAdmins(ctx context.Context, req *pb.AdminListRequest) (*pb.Admins, error) {
	defer helpers.Duration(helpers.Track("ListAdmins"))
	if s.use_caching {
		if res, ok := s.admins_cache[req.CompanyUuid]; ok {
			s.logger.Info("list admins cache hit [company uuid:" + req.CompanyUuid + "]")
			return res, nil
		} else {
			s.logger.Info("list admins cache miss [company uuid:" + req.CompanyUuid + "]")
		}
	}
	_, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "Failed to authorize")
	// }

	// switch authz {
	// case auth.AuthorizationAuthenticatedUser:
	// 	if err = s.PermissionCompanyAdmin(md, req.CompanyUuid); err != nil {
	// 		return nil, err
	// 	}
	// case auth.AuthorizationSupportUser:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	if _, err = s.GetCompany(ctx, &pb.GetCompanyRequest{Uuid: req.CompanyUuid}); err != nil {
		return nil, err
	}

	res := &pb.Admins{CompanyUuid: req.CompanyUuid, Version: 0}

	rows, err := s.db.Query("select user_uuid from admin where company_uuid=?", req.CompanyUuid)
	if err != nil {
		return nil, s.internalError(err, "Unable to query database")
	}

	for rows.Next() {
		var userUUID string
		if err := rows.Scan(&userUUID); err != nil {
			return nil, s.internalError(err, "Error scanning database")
		}
		e, err := s.GetDirectoryEntry(ctx, &pb.DirectoryEntryRequest{CompanyUuid: req.CompanyUuid, UserUuid: userUUID})
		if err != nil {
			return nil, err
		}
		res.Admins = append(res.Admins, *e)
	}

	if s.use_caching {
		s.admins_lock.Lock()
		s.admins_cache[req.CompanyUuid] = res
		s.admins_lock.Unlock()
	}
	return res, nil
}

func (s *companyServer) GetAdminExist(ctx context.Context, req *pb.DirectoryEntryRequest) (*pb.AdminExist, error) {
	if _, err := s.GetCompany(ctx, &pb.GetCompanyRequest{Uuid: req.CompanyUuid}); err != nil {
		return nil, err
	}

	res := &pb.AdminExist{}
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM admin WHERE (company_uuid=? AND user_uuid=?))",
		req.CompanyUuid, req.UserUuid).Scan(&res.Exist)
	if err != nil {
		return nil, s.internalError(err, "failed to query database")
	}
	return res, nil
}

func (s *companyServer) GetAdmin(ctx context.Context, req *pb.DirectoryEntryRequest) (*pb.DirectoryEntry, error) {
	_, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "Failed to authorize")
	// }

	// switch authz {
	// case auth.AuthorizationAuthenticatedUser:
	// 	if err = s.PermissionCompanyAdmin(md, req.CompanyUuid); err != nil {
	// 		return nil, err
	// 	}
	// case auth.AuthorizationSupportUser:
	// case auth.AuthorizationWWWService:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	if _, err = s.GetCompany(ctx, &pb.GetCompanyRequest{Uuid: req.CompanyUuid}); err != nil {
		return nil, err
	}

	var exists bool
	err = s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM admin WHERE (company_uuid=? AND user_uuid=?))",
		req.CompanyUuid, req.UserUuid).Scan(&exists)
	if err != nil {
		return nil, s.internalError(err, "failed to query database")
	} else if !exists {
		return nil, grpc.Errorf(codes.NotFound, "admin relationship not found")
	}
	return s.GetDirectoryEntry(ctx, req)
}

func (s *companyServer) DeleteAdmin(ctx context.Context, req *pb.DirectoryEntryRequest) (*empty.Empty, error) {
	defer helpers.Duration(helpers.Track("DeleteAdmin"))
	md, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "Failed to authorize")
	// }

	// switch authz {
	// case auth.AuthorizationAuthenticatedUser:
	// 	if err = s.PermissionCompanyAdmin(md, req.CompanyUuid); err != nil {
	// 		return nil, err
	// 	}
	// case auth.AuthorizationSupportUser:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	_, err = s.GetAdmin(ctx, req)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("DELETE from admin where (company_uuid=? AND user_uuid=?) LIMIT 1", req.CompanyUuid, req.UserUuid)
	if err != nil {
		return nil, s.internalError(err, "failed to query database")
	}
	al := newAuditEntry(md, "admin", req.UserUuid, req.CompanyUuid, "")
	al.Log(logger, "removed admin")
	go helpers.TrackEventFromMetadata(md, "admin_deleted")

	if s.use_caching {
		if ad, ok := s.admins_cache[req.CompanyUuid]; ok {
			s.admins_lock.Lock()
			var index int
			for i, v := range ad.Admins {
				if v.UserUuid == req.UserUuid {
					index = i
					break
				}
			}
			s.admins_cache[req.CompanyUuid].Admins[index] = ad.Admins[len(ad.Admins)-1]
			s.admins_cache[req.CompanyUuid].Admins = s.admins_cache[req.CompanyUuid].Admins[:len(ad.Admins)-1]
			if !s.use_callback {
				s.admins_cache[req.CompanyUuid].Version = ad.Version + 1
			}
			s.admins_lock.Unlock()
			s.logger.Info("delete admin [company uuid:" + req.CompanyUuid + "]")

			if s.use_callback {
				frontcacheClient, close, err := frontcache.NewClient()
				if err != nil {
					return nil, s.internalError(err, "unable to init frontcache connection")
				}
				defer close()

				_, err = frontcacheClient.InvalidateAdminsCache(ctx, &frontcache.InvalidateAdminsCacheRequest{CompanyUuid: req.CompanyUuid})
				if err != nil {
					return nil, s.internalError(err, "error invalidate FrontCache admins list cache")
				}
			}
		}
	}
	return &empty.Empty{}, nil
}

func (s *companyServer) CreateAdmin(ctx context.Context, req *pb.DirectoryEntryRequest) (*pb.DirectoryEntry, error) {
	md, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "failed to authorize")
	// }

	// switch authz {
	// case auth.AuthorizationAuthenticatedUser:
	// 	if err = s.PermissionCompanyAdmin(md, req.CompanyUuid); err != nil {
	// 		return nil, err
	// 	}
	// case auth.AuthorizationSupportUser:
	// case auth.AuthorizationWWWService:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "you do not have access to this service")
	// }
	_, err = s.GetAdmin(ctx, req)
	if err == nil {
		return nil, grpc.Errorf(codes.AlreadyExists, "user is already an admin")
	} else if grpc.Code(err) != codes.NotFound {
		return nil, s.internalError(err, "an unknown error occurred while checking existing relationships")
	}

	e, err := s.GetDirectoryEntry(ctx, req)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("INSERT INTO admin (company_uuid, user_uuid) values (?, ?)", req.CompanyUuid, req.UserUuid)
	if err != nil {
		return nil, s.internalError(err, "failed to query database")
	}
	al := newAuditEntry(md, "admin", req.UserUuid, req.CompanyUuid, "")
	al.Log(logger, "added admin")
	go helpers.TrackEventFromMetadata(md, "admin_created")

	if s.use_caching {
		if ad, ok := s.admins_cache[req.CompanyUuid]; ok {
			s.admins_lock.Lock()
			s.admins_cache[req.CompanyUuid].Admins = append(ad.Admins, *e)
			if !s.use_callback {
				s.admins_cache[req.CompanyUuid].Version = ad.Version + 1
			}
			s.admins_lock.Unlock()
			s.logger.Info("CreateAdmin updates admins cache [company uuid:" + req.CompanyUuid + "]")

			if s.use_callback {
				frontcacheClient, close, err := frontcache.NewClient()
				if err != nil {
					return nil, s.internalError(err, "unable to init frontcache connection")
				}
				defer close()

				_, err = frontcacheClient.InvalidateAdminsCache(ctx, &frontcache.InvalidateAdminsCacheRequest{CompanyUuid: req.CompanyUuid})
				if err != nil {
					return nil, s.internalError(err, "error invalidate FrontCache admins list cache")
				}
			}
		}
	}
	return e, nil
}

func (s *companyServer) GetAdminOf(ctx context.Context, req *pb.AdminOfRequest) (*pb.AdminOfList, error) {
	_, _, err := getAuth(ctx)
	// if err != nil {
	// 	return nil, s.internalError(err, "Failed to authorize")
	// }

	// switch authz {
	// case auth.AuthorizationAccountService:
	// case auth.AuthorizationWhoamiService:
	// case auth.AuthorizationWWWService:
	// case auth.AuthorizationAuthenticatedUser:
	// 	uuid, err := auth.GetCurrentUserUUIDFromMetadata(md)
	// 	if err != nil {
	// 		return nil, s.internalError(err, "failed to find current user uuid")

	// 	}
	// 	if uuid != req.UserUuid {
	// 		return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// 	}
	// case auth.AuthorizationSupportUser:
	// default:
	// 	return nil, grpc.Errorf(codes.PermissionDenied, "You do not have access to this service")
	// }

	res := &pb.AdminOfList{UserUuid: req.UserUuid}

	rows, err := s.db.Query("select company_uuid from admin where user_uuid=?", req.UserUuid)
	if err != nil {
		return nil, s.internalError(err, "Unable to query database")
	}

	for rows.Next() {
		var companyUUID string
		if err := rows.Scan(&companyUUID); err != nil {
			return nil, s.internalError(err, "err scanning database")
		}
		c, err := s.GetCompany(ctx, &pb.GetCompanyRequest{Uuid: companyUUID})
		if err != nil {
			return nil, err
		}
		res.Companies = append(res.Companies, *c)
	}

	return res, nil
}

func (s *companyServer) GetAdminsVersion(ctx context.Context, req *pb.GetAdminsVersionRequest) (*pb.AdminsVersion, error) {
	if res, ok := s.admins_cache[req.Uuid]; ok {
		return &pb.AdminsVersion{AdminsVer: res.Version}, nil
	}
	return &pb.AdminsVersion{AdminsVer: 0}, nil
	// return nil, fmt.Errorf("GetAdminsVersion not found req uuid")
}
