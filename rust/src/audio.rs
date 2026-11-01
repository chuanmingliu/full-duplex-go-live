//! Sample-level audio operations, compatible with internal/audio in the Go service.
//! The pipeline uses signed PCM16 little-endian mono; G.711 is a wire format.

pub const RATE_TELEPHONY: u32 = 8_000;
pub const RATE_PIPELINE: u32 = 16_000;
pub const RATE_WIDEBAND: u32 = 24_000;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Codec {
    Pcm16,
    MuLaw,
    ALaw,
}

impl Codec {
    pub fn from_mime(mime: &str) -> Result<Self, String> {
        match mime {
            "" | "audio/pcm" | "pcm16" => Ok(Self::Pcm16),
            "audio/pcmu" | "g711_ulaw" => Ok(Self::MuLaw),
            "audio/pcma" | "g711_alaw" => Ok(Self::ALaw),
            _ => Err(format!("unsupported audio format {mime:?}")),
        }
    }
    pub fn mime(self) -> &'static str {
        match self {
            Self::Pcm16 => "audio/pcm",
            Self::MuLaw => "audio/pcmu",
            Self::ALaw => "audio/pcma",
        }
    }
    pub fn bytes_per_sample(self) -> usize {
        if self == Self::Pcm16 { 2 } else { 1 }
    }
    pub fn decode(self, wire: &[u8]) -> Result<Vec<u8>, String> {
        if self == Self::Pcm16 {
            if wire.len() % 2 != 0 {
                return Err("PCM16 audio must contain whole samples".into());
            }
            return Ok(wire.to_vec());
        }
        Ok(wire
            .iter()
            .flat_map(|&b| {
                match self {
                    Self::MuLaw => decode_mu_law(b),
                    Self::ALaw => decode_a_law(b),
                    Self::Pcm16 => unreachable!(),
                }
                .to_le_bytes()
            })
            .collect())
    }
    pub fn encode(self, pcm: &[u8]) -> Vec<u8> {
        if self == Self::Pcm16 {
            return pcm.to_vec();
        }
        pcm.chunks_exact(2)
            .map(|p| {
                let s = i16::from_le_bytes([p[0], p[1]]);
                match self {
                    Self::MuLaw => encode_mu_law(s),
                    Self::ALaw => encode_a_law(s),
                    Self::Pcm16 => unreachable!(),
                }
            })
            .collect()
    }
}

pub fn decode_mu_law(b: u8) -> i16 {
    let b = !b;
    let s = (((i32::from(b & 15) << 3) + 0x84) << ((b >> 4) & 7)) - 0x84;
    if b & 0x80 != 0 { -s as i16 } else { s as i16 }
}
pub fn encode_mu_law(sample: i16) -> u8 {
    let mut s = i32::from(sample);
    let sign = if s < 0 {
        s = -s;
        0x80
    } else {
        0
    };
    s = s.min(32635) + 0x84;
    let mut exponent = 7;
    let mut mask = 0x4000;
    while exponent > 0 && s & mask == 0 {
        exponent -= 1;
        mask >>= 1;
    }
    !(sign | (exponent << 4) | ((s >> (exponent + 3)) & 15)) as u8
}
pub fn decode_a_law(b: u8) -> i16 {
    let b = b ^ 0x55;
    let exponent = (b >> 4) & 7;
    let mantissa = i32::from(b & 15);
    let s = if exponent == 0 {
        (mantissa << 4) + 8
    } else {
        ((mantissa << 4) + 0x108) << (exponent - 1)
    };
    if b & 0x80 == 0 { -s as i16 } else { s as i16 }
}
pub fn encode_a_law(sample: i16) -> u8 {
    let mut s = i32::from(sample);
    let sign = if s < 0 {
        s = -s - 1;
        0
    } else {
        0x80
    };
    s = s.min(32767);
    let compressed = if s >= 256 {
        let mut exponent = 7;
        let mut mask = 0x4000;
        while exponent > 1 && s & mask == 0 {
            exponent -= 1;
            mask >>= 1;
        }
        (exponent << 4) | ((s >> (exponent + 3)) & 15)
    } else {
        s >> 4
    };
    (compressed | sign) as u8 ^ 0x55
}

pub fn decode_pcm16(pcm: &[u8]) -> Result<Vec<f32>, String> {
    if pcm.len() % 2 != 0 {
        return Err("PCM16 buffer has an odd byte count".into());
    }
    Ok(pcm
        .chunks_exact(2)
        .map(|p| f32::from(i16::from_le_bytes([p[0], p[1]])) / 32768.0)
        .collect())
}
pub fn encode_pcm16(samples: &[f32]) -> Vec<u8> {
    samples
        .iter()
        .flat_map(|&s| {
            let v = if s.is_finite() {
                f64::from(s) * 32768.0
            } else {
                0.0
            };
            (v.clamp(-32768.0, 32767.0) as i16).to_le_bytes()
        })
        .collect()
}
pub fn resample(input: &[f32], from_rate: u32, to_rate: u32) -> Vec<f32> {
    if from_rate == to_rate || input.is_empty() {
        return input.to_vec();
    }
    if from_rate == 0 || to_rate == 0 {
        return Vec::new();
    }
    let ratio = f64::from(to_rate) / f64::from(from_rate);
    let n = (input.len() as f64 * ratio) as usize;
    (0..n)
        .map(|i| {
            let src = i as f64 / ratio;
            let lo = src as usize;
            if lo >= input.len() - 1 {
                return input[input.len() - 1];
            }
            let frac = (src - lo as f64) as f32;
            input[lo] * (1.0 - frac) + input[lo + 1] * frac
        })
        .collect()
}
pub fn resample_pcm16(pcm: &[u8], from_rate: u32, to_rate: u32) -> Result<Vec<u8>, String> {
    if from_rate == 0 || to_rate == 0 {
        return Err("sample rates must be positive".into());
    }
    let samples = decode_pcm16(pcm)?;
    if from_rate == to_rate {
        return Ok(pcm.to_vec());
    }
    Ok(encode_pcm16(&resample(&samples, from_rate, to_rate)))
}
pub fn rms(samples: &[f32]) -> f64 {
    if samples.is_empty() {
        return 0.0;
    }
    (samples.iter().map(|&s| f64::from(s).powi(2)).sum::<f64>() / samples.len() as f64).sqrt()
}
pub fn dbfs(rms: f64) -> f64 {
    if rms <= 1e-9 {
        -180.0
    } else {
        20.0 * rms.log10()
    }
}
pub fn zero_crossing_rate(samples: &[f32]) -> f64 {
    if samples.len() < 2 {
        return 0.0;
    }
    samples
        .windows(2)
        .filter(|p| (p[0] >= 0.0) != (p[1] >= 0.0))
        .count() as f64
        / (samples.len() - 1) as f64
}
pub fn bytes_for_ms(rate: u32, ms: u32) -> usize {
    (u64::from(rate) * u64::from(ms) / 1000 * 2) as usize
}
pub fn duration_ms(pcm: &[u8], rate: u32) -> f64 {
    if rate == 0 {
        0.0
    } else {
        pcm.len() as f64 / 2.0 / f64::from(rate) * 1000.0
    }
}

#[derive(Debug)]
pub struct Framer {
    size: usize,
    buffer: Vec<u8>,
}
impl Framer {
    pub fn new(size: usize) -> Result<Self, String> {
        if size == 0 {
            return Err("frame size must be positive".into());
        }
        Ok(Self {
            size,
            buffer: Vec::with_capacity(size * 4),
        })
    }
    pub fn push(&mut self, data: &[u8]) -> Vec<Vec<u8>> {
        self.buffer.extend_from_slice(data);
        let consumed = self.buffer.len() / self.size * self.size;
        let frames = self.buffer[..consumed]
            .chunks_exact(self.size)
            .map(<[u8]>::to_vec)
            .collect();
        self.buffer.drain(..consumed);
        frames
    }
    pub fn flush(&mut self) -> Vec<u8> {
        if self.buffer.is_empty() {
            return Vec::new();
        }
        let mut last = std::mem::take(&mut self.buffer);
        last.resize(self.size, 0);
        last
    }
    pub fn buffered(&self) -> usize {
        self.buffer.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn reference_codes() {
        for (sample, mu, a) in [(0, 0xff, 0xd5), (32767, 0x80, 0xaa), (-32768, 0x00, 0x2a)] {
            assert_eq!(encode_mu_law(sample), mu);
            assert_eq!(encode_a_law(sample), a);
        }
        assert_eq!(decode_mu_law(0xff), 0);
        assert_eq!(decode_a_law(0xd5), 8);
    }
    #[test]
    fn all_companding_codes_round_trip() {
        for code in 0..=255 {
            let code = code as u8;
            assert_eq!(encode_a_law(decode_a_law(code)), code);
            assert_eq!(
                encode_mu_law(decode_mu_law(code)),
                if code == 0x7f { 0xff } else { code }
            );
        }
        for sample in i16::MIN..=i16::MAX {
            for (enc, dec) in [
                (
                    encode_mu_law as fn(i16) -> u8,
                    decode_mu_law as fn(u8) -> i16,
                ),
                (encode_a_law, decode_a_law),
            ] {
                assert!(
                    (i32::from(dec(enc(sample))) - i32::from(sample)).abs()
                        <= i32::from(sample).abs() / 16 + 36
                );
            }
        }
    }
    #[test]
    fn pcm_clips_and_scrubs_non_finite_samples() {
        let encoded = encode_pcm16(&[-2.0, -1.0, 0.0, 1.0, 2.0, f32::NAN, f32::INFINITY]);
        let values: Vec<i16> = encoded
            .chunks_exact(2)
            .map(|x| i16::from_le_bytes([x[0], x[1]]))
            .collect();
        assert_eq!(values, [-32768, -32768, 0, 32767, 32767, 0, 0]);
        assert!(decode_pcm16(&[0]).is_err());
    }
    #[test]
    fn framing_keeps_partial_samples_until_the_next_chunk() {
        let mut f = Framer::new(4).unwrap();
        assert!(f.push(&[1, 2, 3]).is_empty());
        assert_eq!(
            f.push(&[4, 5, 6, 7, 8, 9]),
            vec![vec![1, 2, 3, 4], vec![5, 6, 7, 8]]
        );
        assert_eq!(f.flush(), vec![9, 0, 0, 0]);
        assert!(f.flush().is_empty());
        assert!(Framer::new(0).is_err());
    }
    #[test]
    fn interpolation_and_energy_match_reference() {
        assert_eq!(
            resample(&[0.0, 1.0, 0.0], 8000, 16000),
            [0.0, 0.5, 1.0, 0.5, 0.0, 0.0]
        );
        assert_eq!(zero_crossing_rate(&[1.0, -1.0, 1.0]), 1.0);
        assert_eq!(dbfs(0.0), -180.0);
        assert_eq!(rms(&[1.0, -1.0]), 1.0);
    }
}
