-- Small schema + "real-looking" fake data used by test/e2e-engine.sh (MySQL and MariaDB).
CREATE TABLE country (id INT PRIMARY KEY, name VARCHAR(60) NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE app_user (
  id INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  username VARCHAR(60) NOT NULL, email VARCHAR(120) NOT NULL, phone VARCHAR(30) NULL,
  birthdate DATE NULL, notes TEXT NULL, bio TEXT NOT NULL, payload JSON NULL,
  password VARCHAR(100) NOT NULL, country_id INT NULL,
  UNIQUE KEY uq_username (username),
  CONSTRAINT fk_user_country FOREIGN KEY (country_id) REFERENCES country (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE profile (
  id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, user_id INT NOT NULL, full_name VARCHAR(100) NOT NULL, rut VARCHAR(20) NULL,
  UNIQUE KEY uq_rut (rut),
  CONSTRAINT fk_profile_user FOREIGN KEY (user_id) REFERENCES app_user (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE mail_log (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, body TEXT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO country VALUES (1, 'Chile'), (2, 'Peru');
INSERT INTO app_user (id, username, email, phone, birthdate, notes, bio, payload, password, country_id) VALUES
 (1, '12.345.678-5', 'maria.gonzalez@gmail.com', '+56 9 8765 4321', '1985-06-15', 'escribir a contacto.maria@correo.cl o llamar al +56 9 1111 2222 despues de las 18', 'Soy Maria, vivo en Providencia', '{"hobby":"pintura"}', 'REALHASH-A', 1),
 (2, '9.876.543-2',  'jperez@empresa.cl',         NULL,              NULL,         NULL,                       'Ingeniero civil',                '{"hobby":"ajedrez"}', 'REALHASH-B', 1),
 (3, '15.111.222-K', 'ana.silva@hotmail.com',    '+56 2 2345 6789', '1992-11-03', 'cliente VIP',              '',                               NULL,                  'REALHASH-C', 2);
INSERT INTO profile (id, user_id, full_name, rut) VALUES
 (1, 1, 'Maria Gonzalez Rojas', '12345678-5'), (2, 2, 'Juan Perez Soto', NULL), (3, 3, 'Ana Silva Diaz', '15111222-K');
INSERT INTO mail_log (id, body) VALUES (1, 'Hola Maria, tu clave es 1234'), (2, 'Juan, adjunto tu contrato');
