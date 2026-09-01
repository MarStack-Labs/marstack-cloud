package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type userView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
}

type userListView struct {
	Users []userView `json:"users"`
}

type sessionView struct {
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id"`
	ExpiresAt string `json:"expires_at"`
}

var userHeaders = []string{"EMAIL", "ID", "NAME", "ROLE", "PROJECT", "STATE"}

func userRow(u userView) []string {
	state := "active"
	if u.Disabled {
		state = "disabled"
	}
	return []string{u.Email, u.ID, u.Name, u.Role, u.ProjectID, state}
}

func readPassword(file string) (string, error) {
	if file == "" {
		return "", errors.New("a password is read from a file, not from the command line, " +
			"because a command line is kept in a shell history and shown in ps: " +
			"pass --password-file")
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}

	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return "", errors.New("the password file is empty")
	}
	return password, nil
}

func newUserCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "user",
		Short:   "People who can sign in, and what they may do",
		Aliases: []string{"users"},
	}
	cmd.AddCommand(
		newUserCreateCmd(g),
		newUserListCmd(g),
		newUserGetCmd(g),
		newUserPasswordCmd(g),
		newUserDisableCmd(g),
		newUserEnableCmd(g),
		newUserDeleteCmd(g),
	)
	return cmd
}

func newUserCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Email     string `json:"email"`
		Name      string `json:"name"`
		Role      string `json:"role"`
		ProjectID string `json:"project_id,omitempty"`
		Password  string `json:"password"`
	}
	var passwordFile string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Add somebody who can sign in",
		Long: "Add somebody who can sign in.\n\n" +
			"Their password is read from a file rather than a flag, because a command line\n" +
			"is kept in a shell history and shown in ps. Signing in gives back a token that\n" +
			"carries their role and expires; disabling them stops every token they hold, and\n" +
			"changing their password ends the sessions it replaces.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := readPassword(passwordFile)
			if err != nil {
				return err
			}
			req.Password = password

			var created userView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/users", req, &created,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: userHeaders,
				rows:    [][]string{userRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Email, "email", "", "the address they sign in with")
	cmd.Flags().StringVar(&req.Name, "name", "", "what to call them")
	cmd.Flags().StringVar(&req.Role, "role", "member", "admin, member, or viewer")
	cmd.Flags().StringVar(&req.ProjectID, "project", "", "the project their sessions work in")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "file holding their password")
	must(cmd.MarkFlagRequired("email"))
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("password-file"))

	return cmd
}

func newUserListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the people who can sign in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list userListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/users", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Users))
			for _, one := range list.Users {
				rows = append(rows, userRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: userHeaders, rows: rows})
		},
	}
}

func newUserGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <email|id>",
		Short: "Show one person",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var one userView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/users/"+args[0], nil, &one,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, one, table{
				headers: userHeaders,
				rows:    [][]string{userRow(one)},
			})
		},
	}
}

func newUserPasswordCmd(g *globals) *cobra.Command {
	var passwordFile string

	cmd := &cobra.Command{
		Use:   "password <email|id>",
		Short: "Give somebody a new password, ending the sessions it replaces",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			password, err := readPassword(passwordFile)
			if err != nil {
				return err
			}

			body := struct {
				Password string `json:"password"`
			}{Password: password}

			if err := g.client().do(
				cmd.Context(), "PUT", "/v1/users/"+args[0]+"/password", body, nil,
			); err != nil {
				return err
			}
			cmd.Printf("%s has a new password, and every session made with the old one "+
				"is gone\n", args[0])
			return nil
		},
	}

	cmd.Flags().StringVar(&passwordFile, "password-file", "", "file holding the new password")
	must(cmd.MarkFlagRequired("password-file"))
	return cmd
}

func newUserDisableCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <email|id>",
		Short: "Stop somebody signing in, and stop every token they hold",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendUserAction(cmd, g, args[0], "disable")
		},
	}
}

func newUserEnableCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <email|id>",
		Short: "Let somebody sign in again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendUserAction(cmd, g, args[0], "enable")
		},
	}
}

func sendUserAction(cmd *cobra.Command, g *globals, id, action string) error {
	var changed userView
	if err := g.client().do(
		cmd.Context(), "POST", "/v1/users/"+id+"/"+action, nil, &changed,
	); err != nil {
		return err
	}
	return render(cmd.OutOrStdout(), g.output, changed, table{
		headers: userHeaders,
		rows:    [][]string{userRow(changed)},
	})
}

func newUserDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <email|id>",
		Short: "Remove somebody and every token they hold",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/users/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s and their tokens\n", args[0])
			return nil
		},
	}
}

func newLoginCmd(g *globals) *cobra.Command {
	var (
		email        string
		passwordFile string
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in and print a token that expires",
		Long: "Sign in and print a token that expires.\n\n" +
			"The token is printed rather than saved, so where it is kept is your choice:\n" +
			"export it as MARSTACK_TOKEN, or write it somewhere and pass --token-file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := readPassword(passwordFile)
			if err != nil {
				return err
			}

			body := struct {
				Email    string `json:"email"`
				Password string `json:"password"`
			}{Email: email, Password: password}

			var session sessionView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/login", body, &session,
			); err != nil {
				return err
			}

			if g.output == "json" {
				return render(cmd.OutOrStdout(), g.output, session, table{})
			}
			if _, err := cmd.OutOrStdout().Write([]byte(session.Token + "\n")); err != nil {
				return err
			}
			cmd.PrintErrf("signed in as %s (%s), expires %s\n",
				session.Email, session.Role,
				session.ExpiresAt[:min(len(session.ExpiresAt), 19)])
			return nil
		},
	}

	cmd.Flags().StringVar(&email, "email", "", "the address to sign in with")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "file holding your password")
	must(cmd.MarkFlagRequired("email"))
	must(cmd.MarkFlagRequired("password-file"))

	return cmd
}
