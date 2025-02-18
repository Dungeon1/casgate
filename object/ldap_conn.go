// Copyright 2023 The Casdoor Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/thanhpk/randstr"

	"github.com/casdoor/casdoor/util"
)

type LdapConn struct {
	Conn *goldap.Conn
	IsAD bool
}

//type ldapGroup struct {
//	GidNumber string
//	Cn        string
//}

type LdapUser struct {
	UidNumber string `json:"uidNumber"`
	Uid       string `json:"uid"`
	Cn        string `json:"cn"`
	GidNumber string `json:"gidNumber"`
	// Gcn                   string
	Uuid                  string `json:"uuid"`
	UserPrincipalName     string `json:"userPrincipalName"`
	DisplayName           string `json:"displayName"`
	Mail                  string
	Email                 string `json:"email"`
	EmailAddress          string
	TelephoneNumber       string
	Mobile                string `json:"mobile"`
	MobileTelephoneNumber string
	RegisteredAddress     string
	PostalAddress         string

	GroupId  string `json:"groupId"`
	Address  string `json:"address"`
	MemberOf string `json:"memberOf"`

	Roles []string `json:"roles"`
}

var ErrX509CertsPEMParse = errors.New("x509: malformed CA certificate")

func (ldap *Ldap) GetLdapConn() (*LdapConn, error) {
	var (
		conn *goldap.Conn
		err  error
	)

	if ldap.EnableSsl {
		tlsConf := &tls.Config{}

		if ldap.Cert != "" {
			tlsConf, err = GetTlsConfigForCert(ldap.Cert)
			if err != nil {
				return nil, err
			}
		}

		if ldap.EnableCryptographicAuth {
			var clientCerts []tls.Certificate
			if ldap.ClientCert != "" {
				cert, err := getCertByName(ldap.ClientCert)
				if err != nil {
					return nil, err
				}
				if cert == nil {
					return nil, ErrCertDoesNotExist
				}
				if cert.Scope != scopeClientCert {
					return nil, ErrCertInvalidScope
				}
				clientCert, err := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.PrivateKey))
				if err != nil {
					return nil, err
				}

				clientCerts = []tls.Certificate{clientCert}
			}
			tlsConf.Certificates = clientCerts
		}

		conn, err = goldap.DialTLS("tcp", fmt.Sprintf("%s:%d", ldap.Host, ldap.Port), tlsConf)
	} else {
		conn, err = goldap.Dial("tcp", fmt.Sprintf("%s:%d", ldap.Host, ldap.Port))
	}

	if err != nil {
		return nil, err
	}

	if ldap.EnableSsl && ldap.EnableCryptographicAuth {
		err = conn.ExternalBind()
	} else {
		err = conn.Bind(ldap.Username, ldap.Password)
	}
	if err != nil {
		return nil, err
	}

	isAD, err := isMicrosoftAD(conn)
	if err != nil {
		return nil, err
	}
	return &LdapConn{Conn: conn, IsAD: isAD}, nil
}

func (l *LdapConn) Close() {
	// if l.Conn == nil {
	// 	return
	// }

	// err := l.Conn.Unbind()
	// if err != nil {
	// 	panic(err)
	// }
}

func isMicrosoftAD(Conn *goldap.Conn) (bool, error) {
	SearchFilter := "(objectClass=*)"
	SearchAttributes := []string{"vendorname", "vendorversion", "isGlobalCatalogReady", "forestFunctionality"}

	searchReq := goldap.NewSearchRequest("",
		goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false,
		SearchFilter, SearchAttributes, nil)
	searchResult, err := Conn.Search(searchReq)
	if err != nil {
		return false, err
	}
	if len(searchResult.Entries) == 0 {
		return false, nil
	}
	isMicrosoft := false

	type ldapServerType struct {
		Vendorname           string
		Vendorversion        string
		IsGlobalCatalogReady string
		ForestFunctionality  string
	}
	var ldapServerTypes ldapServerType
	for _, entry := range searchResult.Entries {
		for _, attribute := range entry.Attributes {
			switch attribute.Name {
			case "vendorname":
				ldapServerTypes.Vendorname = attribute.Values[0]
			case "vendorversion":
				ldapServerTypes.Vendorversion = attribute.Values[0]
			case "isGlobalCatalogReady":
				ldapServerTypes.IsGlobalCatalogReady = attribute.Values[0]
			case "forestFunctionality":
				ldapServerTypes.ForestFunctionality = attribute.Values[0]
			}
		}
	}
	if ldapServerTypes.Vendorname == "" &&
		ldapServerTypes.Vendorversion == "" &&
		ldapServerTypes.IsGlobalCatalogReady == "TRUE" &&
		ldapServerTypes.ForestFunctionality != "" {
		isMicrosoft = true
	}
	return isMicrosoft, err
}

func (l *LdapConn) GetLdapUsers(ldapServer *Ldap, selectedUser *User) ([]LdapUser, error) {
	SearchAttributes := []string{
		"uidNumber", "cn", "sn", "gidNumber", "entryUUID", "displayName", "mail", "email",
		"emailAddress", "telephoneNumber", "mobile", "mobileTelephoneNumber", "registeredAddress", "postalAddress",
	}
	if l.IsAD {
		SearchAttributes = append(SearchAttributes, "sAMAccountName")
		SearchAttributes = append(SearchAttributes, "userPrincipalName")
	} else {
		SearchAttributes = append(SearchAttributes, "uid")
	}

	for _, roleMappingItem := range ldapServer.RoleMappingItems {
		SearchAttributes = append(SearchAttributes, roleMappingItem.Attribute)
	}

	var attributeMappingMap AttributeMappingMap
	if ldapServer.EnableAttributeMapping {
		attributeMappingMap = buildAttributeMappingMap(ldapServer.AttributeMappingItems)
		SearchAttributes = append(SearchAttributes, attributeMappingMap.Keys()...)
	}

	ldapFilter := ldapServer.Filter
	if selectedUser != nil {
		ldapFilter = ldapServer.buildAuthFilterString(selectedUser)
	}

	searchReq := goldap.NewSearchRequest(ldapServer.BaseDn, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		0, 0, false,
		ldapFilter, SearchAttributes, nil)
	searchResult, err := l.Conn.SearchWithPaging(searchReq, 100)
	if err != nil {
		return nil, err
	}

	if len(searchResult.Entries) == 0 {
		return nil, errors.New("no result")
	}

	var roleMappingMap RoleMappingMap
	if ldapServer.EnableRoleMapping {
		roleMappingMap = buildRoleMappingMap(ldapServer.RoleMappingItems)
	}

	var ldapUsers []LdapUser
	for _, entry := range searchResult.Entries {
		var user LdapUser
		for _, attribute := range entry.Attributes {
			// check attribute value with role mapping rules
			if ldapServer.EnableRoleMapping {
				if roleMappingMapItem, ok := roleMappingMap[RoleMappingAttribute(attribute.Name)]; ok {
					for _, value := range attribute.Values {
						if roleMappingMapRoles, ok := roleMappingMapItem[RoleMappingItemValue(value)]; ok {
							user.Roles = append(user.Roles, roleMappingMapRoles.StrRoles()...)
						}
					}
				}
			}

			if ldapServer.EnableAttributeMapping {
				MapAttributeToUser(attribute, &user, attributeMappingMap)
				continue
			}

			switch attribute.Name {
			case "uidNumber":
				user.UidNumber = attribute.Values[0]
			case "uid":
				user.Uid = attribute.Values[0]
			case "sAMAccountName":
				user.Uid = attribute.Values[0]
			case "cn":
				user.Cn = attribute.Values[0]
			case "gidNumber":
				user.GidNumber = attribute.Values[0]
			case "entryUUID":
				user.Uuid = attribute.Values[0]
			case "objectGUID":
				user.Uuid = attribute.Values[0]
			case "userPrincipalName":
				user.UserPrincipalName = attribute.Values[0]
			case "displayName":
				user.DisplayName = attribute.Values[0]
			case "mail":
				user.Mail = attribute.Values[0]
			case "email":
				user.Email = attribute.Values[0]
			case "emailAddress":
				user.EmailAddress = attribute.Values[0]
			case "telephoneNumber":
				user.TelephoneNumber = attribute.Values[0]
			case "mobile":
				user.Mobile = attribute.Values[0]
			case "mobileTelephoneNumber":
				user.MobileTelephoneNumber = attribute.Values[0]
			case "registeredAddress":
				user.RegisteredAddress = attribute.Values[0]
			case "postalAddress":
				user.PostalAddress = attribute.Values[0]
			case "memberOf":
				user.MemberOf = attribute.Values[0]
			}
		}

		ldapUsers = append(ldapUsers, user)
	}

	return ldapUsers, nil
}

// FIXME: The Base DN does not necessarily contain the Group
//
//	func (l *ldapConn) GetLdapGroups(baseDn string) (map[string]ldapGroup, error) {
//		SearchFilter := "(objectClass=posixGroup)"
//		SearchAttributes := []string{"cn", "gidNumber"}
//		groupMap := make(map[string]ldapGroup)
//
//		searchReq := goldap.NewSearchRequest(baseDn,
//			goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, 0, false,
//			SearchFilter, SearchAttributes, nil)
//		searchResult, err := l.Conn.Search(searchReq)
//		if err != nil {
//			return nil, err
//		}
//
//		if len(searchResult.Entries) == 0 {
//			return nil, errors.New("no result")
//		}
//
//		for _, entry := range searchResult.Entries {
//			var ldapGroupItem ldapGroup
//			for _, attribute := range entry.Attributes {
//				switch attribute.Name {
//				case "gidNumber":
//					ldapGroupItem.GidNumber = attribute.Values[0]
//					break
//				case "cn":
//					ldapGroupItem.Cn = attribute.Values[0]
//					break
//				}
//			}
//			groupMap[ldapGroupItem.GidNumber] = ldapGroupItem
//		}
//
//		return groupMap, nil
//	}

func AutoAdjustLdapUser(users []LdapUser) []LdapUser {
	res := make([]LdapUser, len(users))
	for i, user := range users {
		res[i] = LdapUser{
			UidNumber:             user.UidNumber,
			Uid:                   user.Uid,
			Cn:                    user.Cn,
			GroupId:               user.GidNumber,
			Uuid:                  user.GetLdapUuid(),
			DisplayName:           user.DisplayName,
			Email:                 util.ReturnAnyNotEmpty(user.Email, user.EmailAddress, user.Mail),
			Mobile:                util.ReturnAnyNotEmpty(user.Mobile, user.MobileTelephoneNumber, user.TelephoneNumber),
			MobileTelephoneNumber: user.MobileTelephoneNumber,
			RegisteredAddress:     util.ReturnAnyNotEmpty(user.PostalAddress, user.RegisteredAddress),
			Address:               user.Address,
			Roles:                 user.Roles,
		}
	}
	return res
}

func SyncLdapUsers(owner string, syncUsers []LdapUser, ldapId string) (existUsers []LdapUser, failedUsers []LdapUser, err error) {
	var uuids []string
	for _, user := range syncUsers {
		uuids = append(uuids, user.Uuid)
	}

	organization, err := getOrganization("admin", owner)
	if err != nil {
		panic(err)
	}

	ldap, err := GetLdap(ldapId)

	var dc []string
	for _, basedn := range strings.Split(ldap.BaseDn, ",") {
		if strings.Contains(basedn, "dc=") {
			dc = append(dc, basedn[3:])
		}
	}
	affiliation := strings.Join(dc, ".")

	var ou []string
	for _, admin := range strings.Split(ldap.Username, ",") {
		if strings.Contains(admin, "ou=") {
			ou = append(ou, admin[3:])
		}
	}
	tag := strings.Join(ou, ".")

	for _, syncUser := range syncUsers {
		existUuids, err := GetExistUuids(owner, uuids)
		if err != nil {
			return nil, nil, err
		}

		found := false
		if len(existUuids) > 0 {
			for _, existUuid := range existUuids {
				if syncUser.Uuid == existUuid {
					existUsers = append(existUsers, syncUser)
					found = true
				}
			}
		}

		name, err := syncUser.buildLdapUserName()
		if err != nil {
			return nil, nil, err
		}

		if !found {
			score, err := organization.GetInitScore()
			if err != nil {
				return nil, nil, err
			}

			newUser := &User{
				Owner:             owner,
				Name:              name,
				CreatedTime:       util.GetCurrentTime(),
				DisplayName:       syncUser.buildLdapDisplayName(),
				SignupApplication: organization.DefaultApplication,
				Type:              "normal-user",
				Avatar:            organization.DefaultAvatar,
				Email:             syncUser.Email,
				Phone:             syncUser.Mobile,
				Address:           []string{syncUser.Address},
				Affiliation:       affiliation,
				Tag:               tag,
				Score:             score,
				Ldap:              syncUser.Uuid,
				Properties:        map[string]string{},
			}

			if organization.DefaultApplication != "" {
				newUser.SignupApplication = organization.DefaultApplication
			}

			affected, err := AddUser(newUser)
			if err != nil {
				return nil, nil, err
			}

			if !affected {
				failedUsers = append(failedUsers, syncUser)
				continue
			}

			userIdProvider := &UserIdProvider{
				Owner:           organization.Name,
				LdapId:          ldapId,
				UsernameFromIdp: syncUser.Uuid,
				CreatedTime:     util.GetCurrentTime(),
				UserId:          newUser.Id,
			}
			_, err = AddUserIdProvider(context.Background(), userIdProvider)
			if err != nil {
				return nil, nil, err
			}
		}

		ldap, err := GetLdap(ldapId)
		if err != nil {
			return existUsers, failedUsers, err
		}

		if ldap.EnableRoleMapping {
			err = SyncRoles(syncUser, name, owner)
			if err != nil {
				return existUsers, failedUsers, err
			}
		}

	}

	return existUsers, failedUsers, err
}

func GetExistUuids(owner string, uuids []string) ([]string, error) {
	var existUuids []string

	err := ormer.Engine.Table("user").Where("owner = ?", owner).Cols("ldap").
		In("ldap", uuids).Select("DISTINCT ldap").Find(&existUuids)
	if err != nil {
		return existUuids, err
	}

	return existUuids, nil
}

func (ldapUser *LdapUser) buildLdapUserName() (string, error) {
	user := User{}
	uidWithNumber := fmt.Sprintf("%s_%s", ldapUser.Uid, ldapUser.UidNumber)
	has, err := ormer.Engine.Where("name = ? or name = ?", ldapUser.Uid, uidWithNumber).Get(&user)
	if err != nil {
		return "", err
	}

	if has {
		if user.Ldap == ldapUser.Uuid {
			return user.Name, nil
		}
		if user.Name == ldapUser.Uid {
			return uidWithNumber, nil
		}
		return fmt.Sprintf("%s_%s", uidWithNumber, randstr.Hex(6)), nil
	}

	if ldapUser.Uid != "" {
		return ldapUser.Uid, nil
	}

	return ldapUser.Cn, nil
}

func (ldapUser *LdapUser) buildLdapDisplayName() string {
	if ldapUser.DisplayName != "" {
		return ldapUser.DisplayName
	}

	return ldapUser.Cn
}

func (ldapUser *LdapUser) GetLdapUuid() string {
	if ldapUser.Uuid != "" {
		return ldapUser.Uuid
	}
	if ldapUser.Uid != "" {
		return ldapUser.Uid
	}

	return ldapUser.Cn
}

func (ldap *Ldap) buildAuthFilterString(user *User) string {
	if len(ldap.FilterFields) == 0 {
		return fmt.Sprintf("(&%s(uid=%s))", ldap.Filter, user.Name)
	}

	filter := fmt.Sprintf("(&%s(|", ldap.Filter)
	for _, field := range ldap.FilterFields {
		filter = fmt.Sprintf("%s(%s=%s)", filter, field, user.getFieldFromLdapAttribute(field))
	}
	filter = fmt.Sprintf("%s))", filter)

	return filter
}

func (user *User) getFieldFromLdapAttribute(attribute string) string {
	switch attribute {
	case "uid":
		return user.Name
	case "sAMAccountName":
		return user.Name
	case "mail":
		return user.Email
	case "mobile":
		return user.Phone
	case "userPrincipalName":
		return user.Email
	default:
		return ""
	}
}

func SyncUserFromLdap(organization string, ldapId string, userName string, password string, lang string) (*LdapUser, error) {
	ldaps, err := GetLdaps(organization)
	if err != nil {
		return nil, err
	}

	user := &User{
		Name: userName,
	}

	for _, ldapServer := range ldaps {
		if len(ldapId) > 0 && ldapServer.Id != ldapId {
			continue
		}

		conn, err := ldapServer.GetLdapConn()
		if err != nil {
			continue
		}

		res, _ := conn.GetLdapUsers(ldapServer, user)
		if len(res) == 0 {
			conn.Close()
			continue
		}

		_, err = CheckLdapUserPassword(user, password, lang)
		if err != nil {
			conn.Close()
			return nil, err
		}

		_, _, err = SyncLdapUsers(organization, AutoAdjustLdapUser(res), ldapServer.Id)
		return &res[0], err
	}

	return nil, nil
}
