// Package tempdb manages the throwaway database container and the data streams in and out of it.
package tempdb

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/letkode/db-anon-dump/internal/engine"
)

// Container is a running temporary database.
type Container struct {
	Name     string
	Password string
	Database string
	HostPort int
	D        engine.Dialect
}

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b), nil
}

func docker(ctx context.Context, args ...string) *exec.Cmd { return exec.CommandContext(ctx, "docker", args...) }

// Start launches the container, publishing the database port on 127.0.0.1 only, with a random password.
func Start(ctx context.Context, d engine.Dialect, image, database string, tmpfs bool, extra []string) (*Container, error) {
	pw, err := randomPassword(24)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("dbanondump-%d-%d", os.Getpid(), time.Now().Unix())
	args := []string{"run", "-d", "--name", name, "--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1::%d", d.ContainerPort())}
	for _, e := range d.ContainerEnv(pw, database) {
		args = append(args, "-e", e)
	}
	if tmpfs {
		args = append(args, "--tmpfs", d.DataDir()+":rw")
	}
	args = append(args, image)
	args = append(args, d.ContainerArgs()...)
	args = append(args, extra...)
	if out, err := docker(ctx, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	c := &Container{Name: name, Password: pw, Database: database, D: d}
	out, err := docker(ctx, "port", name, fmt.Sprintf("%d/tcp", d.ContainerPort())).Output()
	if err != nil {
		c.Remove()
		return nil, fmt.Errorf("cannot read the published port: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	port, err := strconv.Atoi(first[strings.LastIndex(first, ":")+1:])
	if err != nil {
		c.Remove()
		return nil, fmt.Errorf("unexpected docker port output %q", first)
	}
	c.HostPort = port
	return c, nil
}

// DB opens a pooled connection to the temporary database.
func (c *Container) DB() (*sql.DB, error) {
	return sql.Open(c.D.DriverName(), c.D.DSN("127.0.0.1", c.HostPort, c.D.AdminUser(), c.Password, c.Database))
}

// WaitReady blocks until the server answers. The official images restart once after initialising, so two
// consecutive answers a few seconds apart are required.
func (c *Container) WaitReady(ctx context.Context, timeout time.Duration) (*sql.DB, error) {
	db, err := c.DB()
	if err != nil {
		return nil, err
	}
	var last error
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			db.Close()
			return nil, err
		}
		if last = db.PingContext(ctx); last == nil {
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
			if last = db.PingContext(ctx); last == nil {
				return db, nil
			}
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
	db.Close()
	return nil, fmt.Errorf("the temporary database did not become ready within %s (last error: %v)", timeout, last)
}

func (c *Container) execArgs(secret string, cmd []string) []string {
	args := []string{"exec", "-i"}
	if secret != "" {
		args = append(args, "-e", secret)
	}
	args = append(args, c.Name)
	return append(args, cmd...)
}

// Exec runs a command inside the container with the temporary database's secret in its environment.
func (c *Container) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, cmd []string) error {
	var stderr bytes.Buffer
	x := docker(ctx, c.execArgs(c.D.SecretEnv(c.Password), cmd)...)
	x.Stdin, x.Stdout, x.Stderr = stdin, stdout, &stderr
	if err := x.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", cmd[0], err, tail(stderr.String()))
	}
	return nil
}

// Pipe streams the output of srcCmd (run inside the container with srcSecret in its environment, e.g. a
// dump of the source database) into dstCmd (run against the temporary database).
func (c *Container) Pipe(ctx context.Context, srcSecret string, srcCmd, dstCmd []string) error {
	var srcErr, dstErr bytes.Buffer
	src := docker(ctx, c.execArgs(srcSecret, srcCmd)...)
	dst := docker(ctx, c.execArgs(c.D.SecretEnv(c.Password), dstCmd)...)
	pipe, err := src.StdoutPipe()
	if err != nil {
		return err
	}
	src.Stderr, dst.Stdin, dst.Stderr = &srcErr, pipe, &dstErr
	if err := dst.Start(); err != nil {
		return err
	}
	if err := src.Start(); err != nil {
		_ = dst.Process.Kill()
		_ = dst.Wait()
		return err
	}
	srcRun := src.Wait()
	dstRun := dst.Wait()
	if srcRun != nil {
		return fmt.Errorf("%s: %v: %s", srcCmd[0], srcRun, tail(srcErr.String()))
	}
	if dstRun != nil {
		return fmt.Errorf("%s: %v: %s", dstCmd[0], dstRun, tail(dstErr.String()))
	}
	return nil
}

// Remove destroys the container and its volumes. Safe to call more than once.
func (c *Container) Remove() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = docker(ctx, "rm", "-f", "-v", c.Name).Run()
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = "..." + s[len(s)-600:]
	}
	return s
}
